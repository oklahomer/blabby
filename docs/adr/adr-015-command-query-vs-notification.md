# ADR-015: Command/query vs. notification — synchronous request/response or asynchronous best-effort

- **Status:** Accepted
- **Date:** 2026-05-31
- **Related:** [ADR-004](adr-004-message-routing-through-user-grain.md), [ADR-006](adr-006-bidirectional-watch-pattern.md), [ADR-007](adr-007-database-authoritative-persistence.md), [ADR-011](adr-011-cross-boundary-pid-propagation.md), [ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md)

## Context

The system is built from interactions that cross a boundary — an HTTP request into
the gateway, a grain RPC, an actor message, a fan-out to many recipients. When
designing each one, a recurring question decides its shape: does the originator
need to *wait for a result*, or is it merely *announcing that something happened*?

Conflating the two — most often by implementing an announcement as a synchronous
request/response — couples components that need not be coupled, ties one
interaction's latency to another's, and, in a single-threaded actor, can deadlock
when the announcement loops back to a participant still blocked on the original
call.

Two distinct interaction styles recur:

- **Command / query** — a caller acts and *awaits a verdict*: did the join
  succeed, am I already a member, which rooms have I joined. The result is part of
  the interaction's contract; the caller cannot proceed without it.
- **Notification / event** — the system *announces that something happened* (a
  member joined, a message was posted). No caller awaits the outcome; delivery is
  best-effort, and a recipient that misses it reconciles later by re-reading state.

## Decision

**Classify every cross-boundary interaction as either a command/query or a
notification, and shape it accordingly.**

- **Commands and queries are synchronous request/response.** The caller blocks for
  the verdict, which the reply carries. This is the default whenever a result is
  part of the contract.
- **Notifications are asynchronous and best-effort.** They are dispatched off the
  originator's critical path, their completion is never awaited, and **a
  notification must never re-enter or block the originator of the action that
  produced it.** Convergence after a missed notification is the recipient's concern
  — re-reading state on reconnect, or catching up from a durable store — not a
  delivery guarantee of the notification itself.

The litmus test is: *does the originator need the result to proceed?* Yes →
command/query, synchronous. No → notification, asynchronous, fire-and-forget,
non-re-entrant.

This is a design principle, not a single mechanism. The right asynchronous-dispatch
mechanism depends on the boundary — a dedicated worker actor, a one-way message, a
background task. What the principle fixes is the *category*: an announcement is
never modeled as a synchronous call the originator waits on.

## Worked example: Room-grain member fan-out

The Room grain meets both styles, and they show the principle in action.

- **Commands** (join, leave, send) are routed through the user's own grain to the
  Room grain as synchronous request/response — a caller, ultimately the HTTP
  client, awaits the verdict.
- **Notifications** announce room events (joined, left, message) to every member.
  The member set includes the user who triggered the event, so a user's own action
  is echoed to all of their connected devices.

Grains are single-threaded virtual actors: an activation processes one message at a
time, and a synchronous cluster call blocks that activation until the reply
returns. A first cut delivered notifications synchronously, inside the command
handler, before it returned. Because the member set includes the originator, the
notification looped back to the very grain still blocked awaiting its command — a
cycle that could not complete and failed only when the request timeout elapsed.

Applying the principle, member fan-out is a **notification**, so it must not be part
of the command's synchronous round-trip. Each Room grain activation spawns one
long-lived child actor dedicated to fan-out. Command handlers mutate state, snapshot
the recipient set, hand a notification job to the child, and return immediately; the
child performs the per-member calls on its own mailbox, outside the command's call
chain, so no cycle can form. The child processes its mailbox in order, so
notifications are issued in the order the grain produced them — best-effort. Display
order does not depend on delivery timing: clients order and dedup against the durable
`event_id` ([ADR-007](adr-007-database-authoritative-persistence.md),
[ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md)), and the frame's
timestamp is display metadata, not the ordering contract. The child belongs to the
grain's actor hierarchy and stops with it.

### Alternatives considered (for the worked example)

- **A detached goroutine per notification.** Simplest, but the work is unsupervised
  and outside the actor lifecycle, and a fresh goroutine per event adds allocation
  and an ordering hazard, since concurrent goroutines can reorder delivery.
- **Reentry on the command handler** (awaiting a future, then responding). The
  generated grain client exposes only a blocking call and no future, so this path
  requires hand-built request envelopes that couple to generated internals; its one
  benefit — safely touching grain state from the continuation — is unused for
  notification work that only logs.

## Consequences

### Positive

- A shared rule for an everyday design question: when a new interaction is added,
  deciding whether it is a command/query or a notification settles its shape and
  prevents the accidental synchronous-announcement coupling.
- In the worked example: the re-entrancy deadlock is removed; a command returns as
  soon as its state change is committed, and command latency is decoupled from
  notification latency.
- Notification order is preserved by the child's sequential processing; the
  authoritative display order is a second, independent guarantee — clients order and
  dedup against the durable `event_id`, not against delivery timing.
- The fan-out child's lifecycle is automatic: it stops when its Room grain
  passivates.

### Negative / trade-offs

- Notification delivery is not awaited, by definition. A missed notification
  converges only through the recipient re-reading state — on reconnect, or by
  catching up from a durable store where one exists — so the system must provide
  such a path for any state a client must not permanently miss.
- The fan-out child notifies recipients sequentially; a slow or unreachable
  recipient delays later notifications. The design permits per-recipient concurrency
  (preserving per-recipient order) should measurement call for it.

### Neutral

- Command routing is unchanged: still synchronous request/response through the User
  grain.
- No reliance on the generated grain client's internals.

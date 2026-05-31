# ADR-015: Asynchronous member fan-out — Room grain notifies members through a dedicated child actor

- **Status:** Accepted
- **Date:** 2026-05-31
- **Related:** [ADR-004](adr-004-message-routing-through-user-grain.md), [ADR-006](adr-006-bidirectional-watch-pattern.md), [ADR-011](adr-011-cross-boundary-pid-propagation.md)

## Context

Two kinds of grain interaction meet on the Room grain:

- **Commands** (join, leave, send) are routed through the user's own grain to the
  Room grain as synchronous request/response. A caller — ultimately the HTTP
  client — waits for the verdict (joined / already a member / no such room).
- **Notifications** announce room events (joined, left, message) to every member.
  The member set includes the user who triggered the event, so a user's own
  actions are echoed to all of their connected devices.

Grains are single-threaded virtual actors: an activation processes one message at
a time, and a synchronous cluster call blocks that activation until the reply
returns. If the Room grain delivers a notification as a synchronous call back to a
member while that member's grain is still blocked awaiting the original command,
the two calls form a cycle that cannot complete — the member's grain cannot accept
the notification until its command returns, and the command cannot return until the
notification does. Both calls then fail only when the request timeout elapses.

Notification delivery is best-effort and is not part of a command's success
contract: the command's outcome is carried by its own response, and a client that
misses a live notification reconciles by re-reading state on reconnect (and, where
a durable store exists, by catching up from it).

## Decision

Distinguish the two interaction styles explicitly:

- **Commands and queries** (a caller awaits a verdict) remain synchronous
  request/response.
- **Notifications** (best-effort, awaited by no one) are asynchronous and must
  never re-enter their caller.

Each Room grain activation spawns one long-lived child actor dedicated to member
notification. Command handlers mutate state, snapshot the recipient set, hand a
notification job to the child, and return immediately. The child performs the
per-member calls on its own mailbox, outside the command's call chain, so no cycle
can form. The child processes its mailbox in order, so notifications are issued in
the order the grain produced them; clients additionally order messages by the
server-assigned timestamp, making display order independent of delivery timing. The
child belongs to the grain's actor hierarchy and stops with it.

### Alternatives considered

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

- Removes the re-entrancy deadlock; a command returns as soon as its state change is
  committed.
- Command latency is decoupled from notification latency — a command no longer waits
  for every member to be reached.
- Notification order is preserved by the child's sequential processing; client
  timestamp ordering is a second, independent guarantee.
- The child's lifecycle is automatic: it stops when its Room grain passivates.

### Negative / trade-offs

- Notification delivery is not awaited, consistent with best-effort semantics. A
  missed live notification converges only through client re-reads on reconnect, and
  through catch-up from a durable store where one is available.
- The child notifies recipients sequentially; a slow or unreachable recipient
  delays later notifications. The design permits per-recipient concurrency
  (preserving per-recipient order) should measurement call for it.
- Equal-millisecond timestamps are tie-broken by client arrival order; a monotonic
  per-room sequence would order exact ties more strongly.

### Neutral

- Command routing is unchanged: still synchronous request/response through the User
  grain.
- No reliance on the generated grain client's internals.

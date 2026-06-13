# ADR-005: Unconditional fan-out — why Room grain sends to all members regardless of connection state

- **Status:** Accepted
- **Date:** 2026-06-13
- **Related:** [ADR-004](adr-004-message-routing-through-user-grain.md), [ADR-015](adr-015-command-query-vs-notification.md), [ADR-017](adr-017-supervision-strategy.md)

## Context

When a room event occurs — a message is posted, a member joins or leaves —
the Room grain must decide *who to tell*. Two candidate policies:

1. **Filtered fan-out:** the room tracks which members are currently
   connected (or otherwise reachable) and notifies only those.
2. **Unconditional fan-out:** the room notifies every member, full stop, and
   lets each member's side decide what delivery means right now.

The filter looks like an optimization, but it requires the Room grain to hold
a second piece of state — per-member connectivity — that it does not own.
Connectivity is a per-user fact that changes on every connect and disconnect
of every member; the member set, by contrast, changes only on explicit join
and leave commands. Keeping a connectivity view inside every room means
propagating the most frequent event in the system (connection churn) to the
largest set of interested parties (every room the user is in), and being
wrong in the window between change and propagation.

There is also already a component whose job is exactly "what does delivery to
this user mean right now": the User grain owns the live connection set,
kept honest by death-watch
([ADR-012](adr-012-watch-based-connection-lifecycle.md)).

## Decision

**The Room grain fans every room event out to every member's User grain,
unconditionally. Membership is the only criterion. The User grain is the sole
delivery decision point.**

- On each event, the Room grain snapshots its member set
  (`memberIDs()` in `internal/grain/room/state.go` — freshly allocated, so
  later membership mutations cannot race the in-flight fan-out) and hands the
  recipients plus an immutable payload to its fan-out worker
  (`internal/grain/room/fanout.go`).
- The snapshot **includes the member who triggered the event**. The
  originator's own User grain receives the echo and forwards it to all of
  their devices — which is how a message you send from one device appears on
  your others.
- Delivery to each recipient is **best-effort**: a failed per-recipient call
  is logged and skipped (`runNotify` / `runForward` in
  `internal/grain/room/fanout.go`), never retried, and never aborts delivery
  to the remaining recipients.
- The User grain receiving a fan-out forwards it to every connection PID it
  currently holds (`internal/grain/user/sender.go`). A member with zero
  connections is still a delivery target
  at the room level; their User grain simply has nobody to forward to, and the
  event ends there.

This ADR fixes the *policy*. The *mechanism* — fan-out running asynchronously
on a dedicated child actor, off the command's critical path — is owned by
[ADR-015](adr-015-command-query-vs-notification.md), and that child's failure
handling by [ADR-017](adr-017-supervision-strategy.md).

## Consequences

### Positive

- **Room state stays minimal and slow-changing.** Members and recent messages
  only — nothing that mutates on connection churn. The grain's hot path never
  consults connectivity.
- **No stale-presence bugs by construction.** A policy that never filters
  cannot filter wrongly. The class of "missed a message because the room's
  connectivity view lagged" is closed.
- **Self-echo validates the pipeline.** Every command produces a delivery the
  originator can observe, so the send → fan-out → deliver path is exercised on
  every message, not only in multi-party scenarios.
- **The delivery decision sits with the component that owns the facts.** The
  User grain alone knows its connection set; the policy routes the question to
  the single place that can answer it correctly.

### Negative

- **Wasted sends to fully-offline members.** A room with many members and few
  connected pays a User-grain RPC per offline member per event; the cluster
  may activate a member's User grain just to discard the event. Acceptable at
  Phase 1 scale; if measurement ever says otherwise, the fix belongs in
  delivery mechanics (batching, activation-aware dispatch), not in moving
  connectivity state into rooms.
- **Best-effort means missable.** A member whose User-grain call fails simply
  misses that event; convergence relies on re-reading state (reconnect, and a
  durable history once persistence lands) per
  [ADR-015](adr-015-command-query-vs-notification.md).

### Neutral

- Fan-out cost scales with member count, not connection count. Bounding
  room size (a Phase 1 non-goal) is the natural lever if rooms grow large.

## Alternatives considered

### Room tracks connected members and filters

Each room keeps a connected-subset of its member set, updated by
connect/disconnect notifications. Rejected: duplicates the User grain's
authoritative connection knowledge across every room, converts
high-frequency connection churn into cluster-wide state propagation, and is
inevitably stale in the propagation window — a correctness cost taken on to
avoid sends that are merely cheap no-ops today.

### Presence service

A dedicated presence grain/store that rooms query before fanning out.
Rejected: adds a synchronous dependency on the fan-out path and a second
source of connectivity truth that can disagree with the User grain's
death-watch-maintained set. The query either blocks the event (latency) or is
cached (stale again).

### Skip the originator in the snapshot

Echo suppression at the room: don't notify the member who caused the event.
Rejected: the originator's *other devices* must still receive the event, and
the room cannot distinguish devices — only the User grain can. Suppression at
the room level would break multi-device echo; clients that want
suppress-own-echo UX can do it at render time using the sender ID.

## References

- [ADR-004](adr-004-message-routing-through-user-grain.md) — why fan-out
  targets User grains rather than connections.
- [ADR-012](adr-012-watch-based-connection-lifecycle.md) — how the User
  grain's connection set (the delivery decision input) stays honest.
- [ADR-015](adr-015-command-query-vs-notification.md) — fan-out as an
  asynchronous, non-re-entrant notification; the worked example is this
  fan-out.
- [ADR-017](adr-017-supervision-strategy.md) — restart policy for the fan-out
  worker the dispatch runs on.
- `internal/grain/room/fanout.go` — recipient snapshot + best-effort delivery
  loops.

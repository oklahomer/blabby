# ADR-010: Eventual consistency model — why grains are single-writer and reads are eventually consistent

- **Status:** Accepted
- **Date:** 2026-06-13
- **Related:** [ADR-013](adr-013-business-errors-as-response-values.md), [ADR-015](adr-015-command-query-vs-notification.md), [ADR-017](adr-017-supervision-strategy.md)

## Context

State in this system is sharded by domain identity: each User grain owns one
user's facts (connection set, joined rooms), each Room grain owns one room's
facts (member set, recent messages) — see
[ADR-001](adr-001-grain-topology.md). Some domain facts, however, span two
grains. "alice is a member of general" exists twice: in
`general`'s member set (`internal/grain/room/state.go`) and in `alice`'s
joined-rooms set (`internal/grain/user/state.go`).

A system with replicated facts must choose: coordinate writers so the copies
never disagree (transactions, consensus), or accept windows of disagreement
and define how the copies converge. Coordination buys atomicity at the price
of locks or multi-phase protocols spanning grains — machinery that works
against the actor model's core simplification, which is that each actor's
state has exactly one writer (its own mailbox-serialized message handler) and
therefore needs no locks at all. That single-writer guarantee is also why
grain handlers may mutate state directly — the one sanctioned exception to
the project's otherwise-immutable style.

## Decision

**Each grain is the single writer of its own state. Cross-grain facts
converge through message flow rather than transactions, and every read is a
point-in-time answer from exactly one grain. Divergence windows are accepted
and reported honestly rather than masked.**

Concretely:

- **No cross-grain atomicity.** A join command updates the Room grain and the
  User grain in sequence (gateway → User grain → Room grain,
  [ADR-004](adr-004-message-routing-through-user-grain.md)); each update
  commits independently in its owner's mailbox. If a response is lost in
  transit, the two sets can disagree until a later command re-aligns them —
  the system does not roll back the half that succeeded.
- **Reads ask one owner.** `GET /rooms/joined` reads the User grain's set;
  room membership checks read the Room grain's set. A reader observes that
  grain's state *as of its place in that grain's mailbox*, not a global
  snapshot. Two reads against two grains may reflect different moments.
- **Notifications are convergence carriers, not guarantees.** Room events
  propagate best-effort ([ADR-015](adr-015-command-query-vs-notification.md),
  [ADR-005](adr-005-unconditional-fan-out.md)); a missed event is repaired by
  re-reading state, not by delivery retries.
- **Empty state is honest state.** A grain reactivated after passivation or
  node loss starts from `newUserState()` / `newRoomState()` — empty in Phase 1
  (no persistence). The system reports that emptiness truthfully: a post into
  a reactivated room by a formerly-joined user is refused as a business
  outcome ([ADR-013](adr-013-business-errors-as-response-values.md),
  `ROOM_NOT_MEMBER`) rather than served from a pretended continuity. The
  failover integration test
  (`internal/clusterboot/departure_integration_test.go`) pins exactly this
  behavior across a member departure.
- **Idempotent-friendly operations.** Membership mutations are set
  operations (re-join, re-leave, re-register are no-ops by construction in
  both state types), so retries and convergence traffic do not corrupt state.

## Consequences

### Positive

- **No locks, no deadlocks, no coordination protocol.** Correctness of each
  grain's state follows from mailbox serialization alone; the concurrency
  model stays explainable in one sentence.
- **Failure semantics are simple to state and test.** "Single-writer,
  converge by message, report what you have" produces assertable behavior
  under node loss — the departure test encodes it.
- **Scales with the cluster.** Nothing serializes globally; adding members
  adds capacity without widening any coordination domain.
- **The model matches the domain.** Chat tolerates brief staleness (a member
  list a moment old) far better than it tolerates unavailability or blocked
  sends.

### Negative

- **Divergence is real, not theoretical.** A lost Room-grain response leaves
  User and Room membership views disagreeing until the user's next command;
  there is no background reconciler in Phase 1. Consumers must treat either
  view as advisory rather than authoritative-globally.
- **Clients must re-read to heal.** After reconnect or suspected missed
  events, the burden of catching up is the client's (re-fetch joined rooms;
  message history requires Phase 2 persistence,
  [ADR-007](adr-007-single-table-persistence.md)).
- **Phase 1 empty-state honesty is user-visible.** Reactivation forgets
  membership; users re-join. Persistence narrows this window but the
  consistency model — converge, don't coordinate — stays the same.

### Neutral

- This is the standard virtual-actor consistency posture: strong consistency
  *within* a grain (serialized writes, read-your-own-writes per grain), 
  eventual consistency *between* grains.

## Alternatives considered

### Distributed transactions / saga across grains

Wrap join/leave in a two-phase or compensating protocol spanning User and
Room grains. Rejected: imports coordination state, partial-failure
compensation paths, and in-doubt windows — substantial machinery to remove a
divergence that a set-based re-join already heals — and couples two grains'
availability for every membership change.

### Shared store as the source of truth

Keep membership in one database both grains consult. Rejected for Phase 1:
reintroduces the contention and the cache-vs-truth split the actor model
removes (grain state would become a cache of the store, with its own
invalidation problem — see [ADR-008](adr-008-no-redis.md)). Phase 2
persistence ([ADR-007](adr-007-single-table-persistence.md)) makes grain
state *recoverable*, deliberately without making the store a second live
writer.

### Designate one grain as the only owner of the shared fact

E.g. only Room grains know membership; User grains always ask. Rejected: the
joined-rooms query then becomes a scatter-gather over all rooms (the exact
shape [ADR-004](adr-004-message-routing-through-user-grain.md) routes
around), trading a bounded divergence window for an unbounded fan-out on a
common read.

## References

- [ADR-001](adr-001-grain-topology.md) — the ownership sharding this model
  builds on.
- [ADR-013](adr-013-business-errors-as-response-values.md) — the vocabulary
  for reporting honest-but-unwelcome state (`ROOM_NOT_MEMBER` after
  reactivation).
- [ADR-015](adr-015-command-query-vs-notification.md) — best-effort
  notifications and re-read-to-converge.
- [ADR-017](adr-017-supervision-strategy.md) — failure handling consistent
  with empty-state honesty.
- `internal/clusterboot/departure_integration_test.go` — the executable
  statement of this model under node failure.

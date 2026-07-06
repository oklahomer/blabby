# ADR-010: Eventual consistency model — why grains are single-writer and reads are eventually consistent

- **Status:** Accepted
- **Date:** 2026-06-13
- **Related:** [ADR-007](adr-007-database-authoritative-persistence.md), [ADR-013](adr-013-business-errors-as-response-values.md), [ADR-015](adr-015-command-query-vs-notification.md), [ADR-017](adr-017-supervision-strategy.md)

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

- **No cross-grain atomicity.** A join command travels gateway → User grain →
  Room grain ([ADR-004](adr-004-message-routing-through-user-grain.md)); the
  Room grain commits its member-set update first, and the User grain records
  the join only after the room's success response arrives
  (`internal/grain/user/user.go`). Each update commits independently in its
  owner's mailbox: if that response is lost in transit, the room knows a
  member whose own joined-rooms set disagrees, until a later command
  re-aligns them — the system does not roll back the half that succeeded.
- **Reads ask one owner.** `GET /rooms/joined` reads the User grain's set;
  room membership checks read the Room grain's set. A reader observes that
  grain's state *as of its place in that grain's mailbox*, not a global
  snapshot. Two reads against two grains may reflect different moments.
- **Notifications are convergence carriers, not guarantees.** Room events
  propagate best-effort ([ADR-015](adr-015-command-query-vs-notification.md),
  [ADR-005](adr-005-unconditional-fan-out.md)); a missed event is repaired by
  re-reading state, not by delivery retries.
- **Durable facts are restored; only unpersisted facts start empty.** A grain
  reactivated after passivation or node loss hydrates its durable state from the
  authoritative store ([ADR-007](adr-007-database-authoritative-persistence.md)):
  the Room grain reloads its member set on activation, so a formerly-joined user
  posts into a reactivated room without re-joining. What the grain does *not*
  conjure is a fact the store never recorded — a cross-grain command whose response
  was lost mid-flight. The system reports what the store and the grain's own writes
  support, and refuses the rest as an honest business outcome
  ([ADR-013](adr-013-business-errors-as-response-values.md), `ROOM_NOT_MEMBER`,
  implemented in `internal/grain/room/room.go`) rather than pretended continuity.
  The failover integration test
  (`internal/clusterboot/departure_integration_test.go`) pins this across a member
  departure: the reactivated user's joined rooms come back from the store, and a
  send succeeds without a re-join.
- **Idempotent-friendly operations.** Membership mutations are set
  operations (re-join, re-leave, re-register are no-ops by construction in
  both state types), so retries and convergence traffic do not corrupt state.
  The User grain maps Room's already-member/not-member responses to success
  and applies the corresponding local set operation.

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
  there is no background reconciler. Consumers must treat either view as advisory
  rather than authoritative-globally.
- **Clients must retry ambiguous membership commands to heal.** A subsequent
  joined-rooms read refreshes client-local state, but cannot repair a
  Room/User disagreement by itself. Message history is served from the persistent
  journal ([ADR-007](adr-007-database-authoritative-persistence.md)), independent of
  this cross-grain window.
- **The divergence window is narrow, not the whole session.** Reactivation
  restores membership and history from the store, so it no longer forgets them;
  what can still diverge is a cross-grain fact mid-flight, until a later command
  re-aligns the two. The consistency model — converge, don't coordinate — stays the
  same.

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

### A shared store both grains consult live on every read

Keep membership in one database both grains read through on every operation.
Rejected: it reintroduces the contention and the cache-vs-truth split the actor
model removes — grain state becomes a cache of the store with its own invalidation
problem (see [ADR-008](adr-008-no-redis.md)). The design does make the store
authoritative ([ADR-007](adr-007-database-authoritative-persistence.md)), but grains
hydrate from it on activation and then serve reads from their own single-writer
memory; the store is the record of truth, not a second live writer racing the grain
on every request.

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
  with unpersisted-state honesty.
- `internal/clusterboot/departure_integration_test.go` — the executable
  statement of this model under node failure.

# ADR-008: No Redis — why grain memory hydrated from the store replaces a cache tier

- **Status:** Accepted
- **Date:** 2026-07-05
- **Related:** [ADR-007](adr-007-database-authoritative-persistence.md), [ADR-010](adr-010-eventual-consistency-model.md), [ADR-005](adr-005-unconditional-fan-out.md)

## Context

A conventional chat backend reaches for a cache early: hot room state, recent
messages, presence — all the data whose read rate dwarfs its write rate ends up in
Redis, in front of the database, with the application managing the cache's lifecycle
(population, invalidation, TTLs) and its failure modes (dual writes, stale reads,
cold starts).

The actor model changes the premise that makes a cache tier necessary. A virtual
actor *is* a memory-resident, single-writer representation of one entity's hot
state, activated on demand and addressable from anywhere in the cluster
([ADR-001](adr-001-grain-topology.md)). A Room grain holds its member set and a
bounded recent-message window in memory (`internal/grain/room/state.go`); a User
grain holds its connection set and joined rooms (`internal/grain/user/state.go`).
Reads served by a grain are answered from process memory, serialized with the writes
that produce them.

PostgreSQL is now the source of truth for the durable domain
([ADR-007](adr-007-database-authoritative-persistence.md)): a grain hydrates its hot
state from the store when it activates. The question this ADR settles is whether a
cache tier appears **between** the grains and that database — the reflex a
store-of-record usually triggers.

## Decision

**No. A grain's in-memory state is a working copy hydrated from the authoritative
store, and it is also the read path for hot state — there is no separate cache tier,
in any phase. Reads that need hot state ask the grain; reads that need history
beyond the grain's window query PostgreSQL directly. No Redis.**

- **Hot state is served by the owning grain from memory.** Membership, the
  recent-message window, and connection sets are answered by the grain, hydrated
  from the store on activation ([ADR-007](adr-007-database-authoritative-persistence.md)).
  The grain's copy is not a second *independent* writer racing the database — it is
  rebuilt from the record of truth and reflects the grain's own serialized writes.
- **Cold reads go straight to PostgreSQL.** History pagination beyond the
  in-memory window is read from the gateway tier directly against the database.
  Accepting the database's latency for cold queries is the trade; cold queries are
  rare and bounded in a chat workload.
- **Presence-like facts stay with their owning actors.**
  [ADR-005](adr-005-unconditional-fan-out.md) shows the delivery design that avoids
  needing a shared presence store at all.

## Consequences

### Positive

- **The dual-write problem never exists.** Hot state has one live writer — the
  grain, rebuilding from the store and serializing its own updates — so there is no
  invalidation protocol, no stale-cache window, no cache-aside discipline to enforce
  across every write site.
- **One less infrastructure dependency** to deploy, secure, monitor, and explain —
  significant for a reference codebase where every component must earn its place in
  the reader's mental model.
- **Consistency semantics stay uniform.** Reads from grains carry the single-writer
  guarantees of [ADR-010](adr-010-eventual-consistency-model.md); a cache tier would
  add a second, subtly different consistency regime (TTL-bounded staleness) that
  every consumer would have to reason about separately.
- **The hot path is already measured by the actor runtime.** Grain mailbox depth
  and call latency are the observability surface; no separate cache-hit-rate
  dimension required.

### Negative

- **Cold history reads pay full database latency** with no warm layer to absorb
  repeated requests. If a usage pattern emerges where many clients page the same
  history range, the database bears it; the mitigations (indexed reads per
  [ADR-007](adr-007-database-authoritative-persistence.md), PostgreSQL's own buffer
  cache) are real but bounded.
- **Grain memory is per-activation, not shared.** Two grains needing the same
  derived data each hold their own copy (the Room grain's denormalized member refs
  are exactly this). Accepted as the cost of ownership clarity.
- **Scale ceiling moves to grain placement.** A truly hot entity (one room with
  enormous fan-in) concentrates load on one activation rather than being smeared
  across cache replicas. The actor-model answer (sharding the entity itself) is a
  redesign of that entity, not a cache.

### Neutral

- PostgreSQL's internal caching still exists and serves repeated cold reads; "no
  Redis" removes the *application-managed* cache tier, not the database's own memory.

## Alternatives considered

### Redis cache-aside in front of PostgreSQL

The conventional tier: read through the cache, invalidate on write. Rejected: it
duplicates state the grain already holds in memory, and re-imports the exact
problems the actor model dissolves — invalidation correctness, write-ordering
between store and cache, and cold-start stampedes. The cache would be a second copy
of a copy.

### Redis as shared presence / fan-out bus

Use Redis pub/sub to broadcast events and a presence hash to filter delivery.
Rejected: replaces cluster messaging the framework already provides, and
reintroduces the shared-presence-store design that
[ADR-005](adr-005-unconditional-fan-out.md) rejects — a second source of
connectivity truth that can disagree with the User grain's death-watch-maintained
set.

### Per-gateway in-process caches

Cache hot reads in each gateway process. Rejected: N gateways means N
independently-stale copies with no invalidation channel, for reads the owning grain
can already serve cluster-wide at memory speed.

## References

- [ADR-007](adr-007-database-authoritative-persistence.md) — the authoritative store
  this decision declines to put a cache in front of, and the activation-time
  hydration that fills grain memory.
- [ADR-010](adr-010-eventual-consistency-model.md) — the consistency regime
  grain-served reads inherit.
- [ADR-005](adr-005-unconditional-fan-out.md) — the delivery design that removes the
  presence-store temptation.
- `internal/grain/room/state.go` — the in-memory member cache and bounded
  recent-message window that *are* the hot tier.

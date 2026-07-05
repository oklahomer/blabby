# ADR-019: Snowflake ids and worker-lease fencing — time-ordered ids minted safely across a cluster

- **Status:** Accepted
- **Date:** 2026-07-05
- **Related:** [ADR-007](adr-007-database-authoritative-persistence.md), [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md), [ADR-016](adr-016-gateway-backend-tier-separation.md), [ADR-008](adr-008-no-redis.md)

## Context

Every persisted entity and event needs a primary key
([ADR-007](adr-007-database-authoritative-persistence.md)), and the room timeline
needs those keys to be **ordered**: the client pages history and dedups live frames
by comparing ids, so an id must sort by creation time without a separate timestamp
column carrying the ordering. The keys must be mintable by any backend node without a
round-trip to a shared allocator on every write, and — because the cluster runs
multiple backend members ([ADR-016](adr-016-gateway-backend-tier-separation.md)) —
two nodes must **never** mint the same id.

Snowflake ids answer the first two needs directly: a 63-bit integer packing a
timestamp, a worker id, and a per-worker sequence is time-ordered, locally mintable,
and fits a `BIGINT`. The hard part is the third need — guaranteeing worker-id
uniqueness across nodes that start, stop, and fail independently, without trusting a
static per-node configuration and without a clock-skew hole.

## Decision

**Identifiers are 63-bit Snowflakes in one shared number space across users, rooms,
and events. A node mints only while it holds a database-backed worker-id lease, gated
on a local monotonic deadline — so a lost or expired lease fail-closes minting before
another node can reuse the worker id.**

### The id

`internal/snowflake` mints an `int64` laid out high-to-low as:

- **41 bits** milliseconds since a fixed epoch (`2026-01-01T00:00:00Z`, ~69 years of
  range). The epoch is immutable — moving it would renumber every id already minted.
- **10 bits** worker id (`0..1023`), taken from a `worker_lease`.
- **12 bits** per-worker sequence (`0..4095`) within a millisecond.

One generator is bound to one worker id and is goroutine-safe. The value is positive
and strictly increasing per worker; `internal/id` wraps it as the typed `UserID` /
`RoomID` / `EventID` ([ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md))
and renders it as a decimal string on the wire (JavaScript cannot hold 63 bits).

### The fencing

Minting is **fail-closed**. The generator mints only while an injected clock is
before a `leaseDeadline`; a lapsed deadline, a backwards clock, sequence exhaustion
that cannot advance, or an epoch overflow each returns an error instead of risking a
duplicate or out-of-order id.

The deadline is never the lease's database expiry compared against the local wall
clock — an NTP step or host↔database skew could then let a node mint past a lease that
already expired in database time. Instead the lease owner
(`internal/persistence/workerlease`) translates each successful acquire/renew into a
**local monotonic deadline** anchored to the instant the request was *sent* plus
`(ttl − margin)`, and feeds it to the generator. The `worker_lease` table holds one
row per worker id with an `owner` and a `lease_token`; renewal is conditional on
`(worker_id, lease_token, not expired)`, so a node that lost the lease cannot renew
it. The manager heartbeats faster than `ttl − margin`, and its cadence invariants
(`ttl > margin`, `renewInterval < ttl − margin`) are validated at construction, not
discovered at runtime. On a lost lease the manager halts minting and re-acquires a
fresh worker id.

Because each node's local deadline is set conservatively (send-instant plus a margin
below the TTL), a node that fails to renew stops minting *before* its database lease
actually expires — so the worker id is safe for another node to lease.

## Consequences

### Positive

- **Ids are time-ordered and locally mintable.** Timeline pagination and live-frame
  dedup compare ids directly; no write needs a round-trip to a shared allocator.
- **Worker-id uniqueness survives node churn** without static per-node
  configuration: the lease assigns ids dynamically and reclaims them safely after a
  failure.
- **The clock-skew hole is closed by construction.** Minting is bounded by a local
  monotonic deadline, never by comparing a database timestamp to a wall clock, so NTP
  steps and host↔database skew cannot produce a duplicate id.
- **Failure is safe, not silent.** A node that cannot hold its lease stops minting
  and surfaces an error, rather than continuing to mint ids that might collide.

### Negative

- **A backend node cannot mint without a database-reachable lease.** Losing the
  database costs id generation, not just persistence — acceptable, since a node that
  cannot reach the store cannot do useful work anyway.
- **1024 worker ids is a ceiling.** The 10-bit field caps concurrently-leased
  minters; ample for this system, but a hard bound the layout fixes.
- **The epoch is permanent.** The 41-bit window lasts ~69 years from 2026; the epoch
  can never move without renumbering existing ids.

### Neutral

- **Clock regression within a node fails a mint rather than blocking.** The generator
  returns an error on a backwards clock; the caller retries once the clock recovers.
- **Seed fixtures occupy low ids.** Deterministic fixtures use small ids (users 1–3,
  rooms 4–5); a real generator mints far above these at any real time after the
  epoch, so they never collide.

## Alternatives considered

### Random UUIDs (v4)

128-bit random identifiers. Rejected: not time-ordered, so they scatter across the
primary-key index (poor locality on an append-heavy timeline) and cannot serve keyset
pagination or live-frame dedup by comparison. Twice the width of a `BIGINT` for no
ordering benefit.

### Database sequences / auto-increment keys

Let PostgreSQL allocate each id. Rejected: a round-trip to the database for every id
on the write path, a single global sequence as a write-ordering point, and a
monotonic counter that leaks total volume. Snowflake keeps allocation node-local.

### Static per-node worker-id configuration

Assign each node a fixed worker id via configuration. Rejected: a misconfiguration
that hands two nodes the same worker id collides ids silently, and reassigning ids as
nodes scale up and down becomes an operational burden. The lease makes worker-id
assignment dynamic and self-correcting.

### Unfenced Snowflake trusting the lease's database expiry

Mint until the lease's stored `expires_at` passes, judged against the local clock.
Rejected: it reopens the clock-skew hole — a node whose clock lags the database, or
that suffers an NTP step, could mint past a lease that has already expired in database
time, letting another node reuse the worker id and collide. The local monotonic
deadline is precisely the fix.

### A central id-allocation service

A dedicated service (or Redis `INCR`) hands out ids. Rejected: a new infrastructure
dependency and a per-write round-trip, against the grain of a system that
deliberately avoids extra tiers ([ADR-008](adr-008-no-redis.md)). PostgreSQL already
holds the lease; the id itself is minted locally.

## References

- [ADR-007](adr-007-database-authoritative-persistence.md) — the `BIGINT` primary
  keys these ids fill.
- [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md) — the typed
  `UserID` / `RoomID` / `EventID` wrappers over the minted `int64`.
- [ADR-016](adr-016-gateway-backend-tier-separation.md) — the backend members that
  run generators.
- `internal/snowflake/generator.go` — the layout and the fail-closed mint.
- `internal/persistence/workerlease/` — the lease acquire/renew loop and the local
  monotonic deadline.

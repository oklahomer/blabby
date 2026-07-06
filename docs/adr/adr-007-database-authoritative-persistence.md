# ADR-007: Database-authoritative persistence — normalized entities plus an append-only event journal

- **Status:** Accepted
- **Date:** 2026-07-05
- **Related:** [ADR-008](adr-008-no-redis.md), [ADR-010](adr-010-eventual-consistency-model.md), [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md), [ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md), [ADR-020](adr-020-pgroonga-search-stack.md)

## Context

The durable domain of this system is small and well-shaped: accounts, rooms, who
belongs to which room, and each room's timeline of messages and membership events.
Grains ([ADR-001](adr-001-grain-topology.md)) serve this data from memory at
request time, but memory does not survive passivation, a node loss, or a restart.
Something must hold the truth across those events, and its shape drives the write
path, the read path, and the operational story of the whole system.

Two shaping questions have to be answered together. First, **what is authoritative**
— the grain's in-memory state, or a store? Second, **what shape** does the store
take: relational tables per entity, or an event-sourced log, or a generic blob?
The event payloads vary by type (a posted message, a member join, a member leave
carry different fields), which tempts either a table per event type or an opaque
blob — each with a familiar failure mode: schema sprawl on one side, un-queryable
data on the other.

## Decision

**PostgreSQL is the source of truth for the durable domain. Entities are normalized
relational tables; the room timeline is one append-only `event` table with a JSONB
payload for the type-specific fields. Grains hold a working copy of hot state
hydrated from the store on activation, and remain the single writer of that copy.**

Concretely (the schema is `internal/persistence/schema.sql`):

- **Authoritative store, hydrating grains.** The database holds the record of
  truth for accounts (`service_user`), rooms (`room`), current membership
  (`room_membership`), and the timeline (`event`). A grain seeds its hot state from
  the store when it activates — the Room grain fills its reference metadata via
  `RoomLoader` and its member cache via `MembershipStore.LoadMembers`
  (`internal/grain/room/`). The grain then serves reads from memory and reflects
  its own writes, so membership and history **survive reactivation**. The store is
  the record of truth; the grain is a working copy rebuilt from it, not a second
  independent writer ([ADR-008](adr-008-no-redis.md),
  [ADR-010](adr-010-eventual-consistency-model.md)).
- **Normalized entities with invariants at the boundary.** Accounts, rooms, and
  membership are ordinary relational tables with foreign keys and `NOT NULL`
  columns that express domain invariants where the database can enforce them:
  `service_user.password_hash` is `NOT NULL` (no account, seed or otherwise, lacks
  a real hash); a partial unique index enforces at-most-one `owner` per room; named
  unique constraints let the repositories classify a duplicate registration as
  `EMAIL_ALREADY_REGISTERED` versus `HANDLE_ALREADY_TAKEN` without depending on
  Postgres's implicit constraint names.
- **One append-only journal for the timeline.** The `event` table carries the keys
  every timeline query needs as normalized columns — `id`, `room_id`, `type`,
  `user_id`, `occurred_at` — and the type-specific fields in a single `JSONB`
  `payload` (a message's text; the actor's display name on a membership event).
  The Room grain is the single appender per room, so within one activation the
  ids it mints increase and order the timeline; across a reactivation onto
  another backend node, that ordering rests on the cluster's clock-sync
  assumption ([ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md)).
  `occurred_at` is the single DB clock, stamped `now()`, and is display-only
  today. A reserved `client_key` unique index is the affordance for send
  idempotency.
- **Native enums, Snowflake keys.** Enum-like columns (`user_status`,
  `room_status`, `membership_role`, `event_type`) use native PostgreSQL `ENUM`
  types for readable predicates (`role = 'owner'`) and database-level type safety,
  each mirrored by a Go typed enum. Primary keys are Snowflake `BIGINT`s in one
  shared number space across users, rooms, and events
  ([ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md)); the opaque
  public code (`U…` / `R…`) is the external form
  ([ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md)).
- **One schema file, recreated from scratch.** The schema lives in a single
  `schema.sql` applied two ways from the same file — mounted at the Postgres
  docker-entrypoint so `make up` provisions a ready database on a fresh volume, and
  exec'd by integration tests against a clean database. It is recreated from
  scratch, never altered in place, so there is no second copy to drift and no
  migration tool: the usual enum-evolution and migration friction does not apply
  when the schema is always rebuilt.

## Consequences

### Positive

- **State survives reactivation.** Membership and history outlive passivation and
  node loss; the Phase-1 posture of an empty grain after reactivation
  ([ADR-010](adr-010-eventual-consistency-model.md)) is retired for the
  DB-authoritative facts.
- **Invariants live where the database can enforce them.** Foreign keys, `NOT
  NULL`, and partial unique indexes make whole classes of invalid state
  unrepresentable, independent of application correctness.
- **One schema, no drift.** Demo provisioning and test isolation apply the same
  file, so the schema a test runs against is the schema `make up` ships.
- **A queryable timeline.** Normalized columns serve timeline pagination
  (`(room_id, id DESC)`) and the membership gate directly; the JSONB payload keeps
  the message text where a PGroonga full-text index can cover it
  ([ADR-020](adr-020-pgroonga-search-stack.md)) without a projection tier.
- **Adding an event type is a code change, not a migration.** The JSONB payload
  absorbs new fields; the append statement and the timeline read stay one shape.

### Negative

- **Schema evolution means recreating the database.** With no migration tool and a
  recreate-from-scratch model, changing the schema drops and rebuilds the data. This
  suits a pre-production reference system where fixtures are re-seeded freely; a
  deployment that must preserve live data would need a migration path this design
  does not provide.
- **JSONB payloads are schema-checked only at the application layer.** A bug can
  store a malformed payload the database accepts; detection moves to read time.
  Mitigated by payloads being marshalled from typed values in one journal package.
- **One table concentrates timeline growth.** All rooms' events share `event`,
  whose volume is dominated by messages; partitioning or archival becomes that
  table's eventual concern rather than being spread across per-type tables.

### Neutral

- **Read models can still be added later.** Nothing forbids a materialized
  projection if a query outgrows JSONB filtering; the decision is not to *start*
  with one.
- **JSONB is PostgreSQL-specific.** Portability to another store is reduced —
  accepted, since PostgreSQL is the deliberate default and the repositories are the
  swap seam.

## Alternatives considered

### Event sourcing via the actor framework's persistence middleware

Capture each grain's state changes as events through Proto.Actor's persistence
provider and replay them on activation. Rejected: the durable facts here are
current-state questions (who is in this room, what is this account) that a
normalized row answers directly and legibly, without a replay-and-fold step on
every activation. The timeline *is* an append-only log, but it is one table the
Room grain appends to, not the grain's whole state rebuilt from an event stream.
Event sourcing buys audit and time-travel this system does not need, at the cost of
a projection or fold for every read.

### A migration tool (e.g. golang-migrate)

Version the schema as ordered up/down migrations. Rejected while the schema is
recreated from scratch: migrations earn their keep by evolving a database whose data
must be preserved, which is exactly what recreate-from-scratch declines to do. A
single readable `schema.sql` is the honest artifact for a system that re-seeds its
fixtures; a migration tool is the right answer once preserving live data becomes a
requirement.

### Table per event type (fully normalized timeline)

Give each event type its own typed columns. Strong per-type enforcement, but the
timeline read becomes a UNION across tables ordered by a shared sequence, every new
event type is a schema change, and the append path forks per type. The enforcement
benefit accrues to the layer that already has types — the application — while the
costs land on the layer that does not need them.

### Opaque blob store (no normalized columns)

Serialize the whole event and key by id only. Minimal schema, but timeline
pagination and the membership/message-text filters would require reading and
deserializing everything; the normalized columns exist precisely because those
filters are already known query needs.

### Grains as the sole authority (no store of truth)

Keep the Phase-1 model where grain memory is authoritative and the database, if any,
is a replay log. Rejected: membership and history must survive reactivation as a
product requirement, and honest empty-state after reactivation
([ADR-010](adr-010-eventual-consistency-model.md)) is a poor experience once the
data exists to restore. Making the store authoritative and hydrating grains from it
keeps the single-writer simplicity while giving durability.

## References

- [ADR-008](adr-008-no-redis.md) — the companion decision: grain memory is a
  working copy hydrated from this store, with no cache tier between.
- [ADR-010](adr-010-eventual-consistency-model.md) — single-writer grains and the
  consistency model this persistence hydrates into.
- [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md) — identifier
  types; the Snowflake `BIGINT` is the internal primary key, the public code the
  external form.
- [ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md) — how the `BIGINT`
  keys are minted across a cluster.
- [ADR-020](adr-020-pgroonga-search-stack.md) — the full-text search this schema's
  PGroonga indexes serve.
- `internal/persistence/schema.sql` — the schema this ADR describes.
- `internal/persistence/{userrepo,roomrepo,membershiprepo,journal,verifyrepo}` and
  `internal/grain/room/` — the write paths and the activation-time hydration.

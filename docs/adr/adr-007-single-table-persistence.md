# ADR-007: Single-table persistence — why normalized columns + JSONB in one table

- **Status:** Proposed
- **Related:** [ADR-008](adr-008-no-redis.md), [ADR-010](adr-010-eventual-consistency-model.md)

## Context

Phase 1 runs without persistence: grain state lives in memory and is rebuilt
from traffic after passivation or failure
([ADR-010](adr-010-eventual-consistency-model.md)). `internal/persistence/`
exists as a placeholder. Phase 2 adds durability — message history that
survives reactivation, membership that survives restarts — and the storage
schema is a decision worth fixing before any of it is built, because schema
shape drives the write path, the query path, and the migration story.

The planned mechanism is event sourcing through Proto.Actor's persistence
middleware: a grain's state changes are captured as events, and a provider
implementing the `ProviderState` interface stores and replays them. PostgreSQL
is the chosen backing store, with `golang-migrate` for schema migrations.
What remains open is how events land in PostgreSQL. Event payloads vary by
type (a posted message, a member join, a member leave carry different
fields), which tempts either a table per event type or a generic blob store —
each with a familiar failure mode: schema sprawl on one side, opaque data on
the other.

## Decision

**All events go into one table: a small set of normalized columns for the
keys every query needs, and a JSONB column for the type-specific payload.**

The intended shape:

- Normalized columns carry what indexing and replay require: the grain's
  identity (kind + id), a per-grain monotonically increasing sequence number,
  the event type name, and the server-assigned timestamp (`BIGINT` Unix
  milliseconds, matching the client protocol's timestamp form; grain protos
  internally carry `google.protobuf.Timestamp`).
- The event's type-specific fields live in one `JSONB` column, written and
  read by the provider as the serialized event.
- Replay for one grain is a single indexed range scan: by grain identity,
  ordered by sequence. History queries (e.g. recent messages for a room)
  read the same table filtered by grain identity and event type.
- One write path: every grain's provider appends to the same table with the
  same statement shape. Migrations evolve one schema.

This ADR stays **Proposed** until Phase 2 implements the PostgreSQL
provider; it records the direction so the provider work starts from a settled
schema philosophy rather than re-opening it.

## Consequences

### Positive

- **One write path, one migration target.** Adding an event type is a code
  change, not a schema change — the JSONB payload absorbs it. The append
  statement, the replay query, and the backup story are written once.
- **Replay is structurally simple.** Grain recovery is "scan my rows in
  order", with the index that serves it obvious and singular.
- **Queryability without projection infrastructure.** PostgreSQL's JSONB
  operators allow ad-hoc inspection and even indexed payload queries if a
  hot path emerges — useful headroom for a system whose query needs are
  still small.
- **Matches the consistency model.** Each grain appends only its own events
  (single writer, [ADR-010](adr-010-eventual-consistency-model.md)); a
  per-grain sequence in one table needs no cross-grain coordination.

### Negative

- **Payloads are schema-checked only at the application layer.** A bug can
  write a malformed payload that the database happily stores; detection
  moves to deserialization time. Mitigated by payloads being produced from
  typed events in one provider.
- **One table concentrates growth.** All history shares a table whose volume
  is dominated by message events; partitioning or archival becomes that
  table's eventual concern rather than being spread across per-type tables.
- **JSONB is PostgreSQL-specific.** The provider's portability to another
  store is reduced — accepted, since PostgreSQL is the deliberate default
  and the `ProviderState` interface remains the swap seam.

### Neutral

- Read models can still be added later. Nothing in the single-table design
  forbids materialized projections if a query outgrows JSONB filtering; the
  decision is to not *start* with them.

## Alternatives considered

### Separate event store and projection tables (full CQRS)

Append-only event table plus per-view read models kept in sync by
projectors. The textbook shape for event sourcing at scale — and double
bookkeeping this system's query needs don't justify: every projection adds a
consumer, a lag window, and an operational surface. Rejected as a starting
point; remains the documented growth path if reads demand it.

### Table per event type (fully normalized)

Each event type gets typed columns in its own table. Strong per-type
schema enforcement, but replay becomes a UNION across N tables ordered by a
shared sequence, every new event type is a migration, and the write path
forks per type. The enforcement benefit accrues to the layer that already
has types — the application — while the costs land on the layer that
doesn't need them.

### Opaque blob store (no normalized columns)

Serialize the whole event, key by grain id + sequence only. Minimal schema,
but history queries beyond replay (messages for a room, events since T)
require reading and deserializing everything; the timestamp and event-type
columns exist precisely because those two filters are already known query
needs.

## References

- [ADR-008](adr-008-no-redis.md) — the companion decision: grain memory is
  the hot path; this table is the cold truth, with no cache tier between.
- [ADR-010](adr-010-eventual-consistency-model.md) — single-writer grains,
  which make the per-grain sequence sound.
- `internal/persistence/` — the placeholder this design will fill.

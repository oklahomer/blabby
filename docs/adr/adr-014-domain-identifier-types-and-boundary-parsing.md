# ADR-014: Domain identifier types and boundary parsing

- **Status:** Accepted
- **Date:** 2026-07-05
- **Related:** [ADR-007](adr-007-database-authoritative-persistence.md), [ADR-011](adr-011-cross-boundary-pid-propagation.md), [ADR-013](adr-013-business-errors-as-response-values.md), [ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md)

## Context

Two identifiers are load-bearing across every layer of this system: the
authenticated user and the chat room. Both flow through HTTP path captures, JWT
subject claims, WebSocket auth frames, cluster proto requests, grain state, database
columns, and structured log lines. Represented as raw `string` (or raw `int64`),
they invite three problems: a `user`/`room` mix-up that compiles and runs, structural
validation duplicated and drifting across call sites, and — for a persistent system —
the question of whether the database's numeric primary key is the thing that should
travel on the wire to clients at all.

That last question is the one persistence forces. The store keys entities by a
Snowflake `BIGINT` ([ADR-007](adr-007-database-authoritative-persistence.md),
[ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md)). Exposing that number
to clients would leak an internal, roughly-enumerable key and weld the wire format to
the storage key. So the system needs two representations — an internal one for
routing and storage, and an external one for clients — and a discipline that keeps
them from being confused for each other.

## Decision

**Identifiers are value-object types, distinct at the Go type level and parsed once
at each boundary. Users and rooms carry two: an internal numeric Snowflake id for
routing and storage, and a separate opaque `PublicCode` — the only user/room
identifier shown to clients — resolved to the internal id at the gateway.**

The `internal/id` package (`internal/id/doc.go`) holds these types:

- **Internal ids: `UserID`, `RoomID`, `EventID`.** Each wraps a positive 63-bit
  `int64` minted by `internal/snowflake`, distinct at the type level so a function
  taking a `UserID` rejects a `RoomID`, an `EventID`, or a bare `int64` at compile
  time. `NewUserID`/`NewRoomID`/`NewEventID` wrap a value read from a `BIGINT`
  column; `ParseUserID`/`ParseRoomID`/`ParseEventID` decode the **decimal-string**
  form carried in proto fields and JSON. Where an id serialises into JSON it renders
  as a decimal string, because JavaScript cannot hold a 63-bit integer exactly. A
  zero value is not valid; the constructors reject non-positive input and never emit
  one.
- **External reference: `PublicCode`.** An opaque, user-facing code rendered
  `U<code>` for users and `R<code>` for rooms (the type letter is added only at the
  edge). It is stored alongside the entity, is the **only** user/room identifier a
  client ever sees, and is not part of a `UserID`/`RoomID` value. `EventID` is the
  one id that itself crosses to clients — as a decimal-string cursor — and so has no
  public code.
- **Composed reference: `UserRef`.** Bundles a `UserID` (internal), a `PublicCode`
  (external), and a display name, for the places that need to render a person
  without a second lookup (message senders, membership events).
- Every id type implements [`slog.LogValuer`](https://pkg.go.dev/log/slog#LogValuer)
  so structured-log call sites pass the typed value directly and it renders as its
  decimal string (the JSON handler would otherwise reflect over the unexported field
  and lose it).

Raw scalars for an identifier appear only at these boundaries; internal code holds
typed values throughout:

1. **JWT verification.** The token's subject is the user's `PublicCode` (`U…`), never
   the numeric id. `auth.JWTAuthenticator` parses it with `id.ParseUserCode` and
   resolves it to an internal `UserID` via a `PublicCodeResolver`; an unknown code is
   an invalid token, a resolver outage is answered `503` rather than treated as
   invalid.
2. **HTTP path extraction.** A room path capture (`{id}`) is a client-facing `R…`
   code; the gateway parses it with `id.ParseRoomCode` and resolves it to a `RoomID`
   through the room directory (`FindByPublicCode`). A malformed code is `INVALID_REQUEST`,
   an unknown one `ROOM_NOT_FOUND`.
3. **Cluster proto entry.** Proto fields carry the internal numeric id as a decimal
   string; each grain handler parses `req.GetUserId()` / `req.GetRoomId()` into typed
   values at the top of the function, because its contract is "any caller within the
   cluster," not "only the gateway."
4. **Storage.** The repositories wrap `BIGINT` column values with the `New…`
   constructors and bind typed values back out with `.Int64()`.

Internal state and signatures carry the types through: the User grain's joined set is
`map[id.RoomID]struct{}`, the Room grain's member set keys on `id.UserID`, and
cross-keying is a compile error. The types prove only that an id is structurally
well-formed (a positive Snowflake, or a syntactically valid public code); they do not
claim the user or room exists, that the caller is authorised, or that membership
holds — those belong to the grains and handlers that own the rules.

## Consequences

### Positive

- **Mix-ups become compile errors.** `func notify(uid UserID, rid RoomID)` cannot be
  called with its arguments swapped, and neither can be passed a bare `int64` — the
  most common identifier bug is removed by the type system.
- **Internal keys never leak.** Clients see opaque public codes, not the Snowflake
  primary key; the wire format is decoupled from the storage key, and the resolution
  seam lives in one place (the gateway).
- **Parse once, trust thereafter.** Once a value is a `UserID`, no callee
  re-validates it. "Parse, don't validate" is enforced by the type system, and the
  two representations can never be silently interchanged because they are different
  types.
- **Logs keep the identifier.** `slog.LogValuer` renders the decimal string on JSON
  log lines instead of an empty reflected struct.

### Negative

- **Two representations mean a resolution step.** A client-facing public code must be
  resolved to an internal id at the gateway (a database lookup) before routing. The
  cost is one indexed read at the boundary, concentrated where the translation
  belongs; internal hops stay numeric.
- **Conversions at the proto wire.** Every proto-construction site writes the id's
  decimal string and every receipt site re-parses. The cost is constant and
  concentrated at the cluster boundary.
- **Grain handlers re-parse ids from incoming requests.** A request through the
  gateway is parsed twice. The grain's contract is the cluster boundary; any future
  cluster caller inherits the same checks without the grain trusting the caller's
  discipline.

### Neutral

- **`EventID` is the deliberate exception.** It crosses to clients directly as a
  decimal-string cursor, with no public code, because a timeline cursor is already
  opaque and reveals nothing an attacker can act on; giving it a public code would be
  ceremony without benefit.
- **`internal/id` imports `log/slog`.** A non-trivial choice for a value-object
  package, but slog is standard-library and `LogValuer` is the idiomatic bridge; the
  import is one-way.

## Alternatives considered

### Expose the numeric primary key to clients

Send the Snowflake `BIGINT` on the wire and skip the public code. Rejected: it leaks
an internal, time-ordered, roughly-enumerable key, and binds the external contract to
the storage key so the two can never evolve independently. The public code is the
opaque, stable external handle; the numeric id stays internal.

### Per-package identifier types

Each consumer defines its own `UserID`/`RoomID`. Rejected: it repeats the duplication
the error-code constants once suffered and loses the cross-boundary equality that map
keys and typed parameters need — a `connection.UserID` cannot key a grain-side
`map[grain.UserID]struct{}` without a re-import of the validation question.

### A single generic `Identifier[T]` with phantom types

`type UserID = Identifier[userTag]`. Identical mechanism, fewer file-level types.
Rejected as too clever for this codebase: `id.NewUserID(raw)` reads immediately, while
`id.NewIdentifier[id.UserTag](raw)` asks the reader to unpack a generic for no gain.

### Proto-typed wire identifiers (`message UserId { string value = 1; }`)

Wrap each id in a nested proto message. Rejected: a nested message is a struct with a
string field — no constructor, no validation hook, and a parallel hierarchy of "Id
message types" mirroring the Go types. The wire format is the wire format; the type
discipline belongs in Go.

### Storage-backed `Room` / `User` aggregates instead of identifier types

Skip the identifier types and pass whole entities around. Still declined for the hot
paths: join, leave, and post-message care about the identifier, not the room's
description. The storage-backed entities now exist in the persistence layer
([ADR-007](adr-007-database-authoritative-persistence.md)) and sit alongside the
identifier types without a name collision — `RoomID` (a reference) and the persisted
room row (an entity) are distinct concepts that never compete for the same name.

## Scope of applicability

This ADR governs the representation of user and room identifiers, and the public code
that fronts them. It does **not** govern:

- **Cluster identity strings.** `cluster.Get…GrainClient(c, identity)` takes a
  `string` identity opaque to the framework; the gateway converts a typed id to its
  decimal string at that seam.
- **Other domain-significant strings.** Authentication tokens, message text, handles,
  and email addresses are not covered; each has (or gets) its own value-object
  discipline where its rules live.

## References

- [ADR-007](adr-007-database-authoritative-persistence.md) — the persistence layer
  whose `BIGINT` keys are the internal ids and whose `public_code` columns store the
  external form.
- [ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md) — how the numeric ids
  are minted across a cluster.
- [ADR-011](adr-011-cross-boundary-pid-propagation.md) — the same "parse once at the
  boundary, hold typed values internally" principle applied to actor identities.
- [ADR-013](adr-013-business-errors-as-response-values.md) — the `INVALID_REQUEST`
  code that identifier-parse failures map to.
- `internal/id/` — the value-object types; `internal/auth/jwt.go` and
  `internal/gateway/handler_room.go` — the public-code resolution boundaries.

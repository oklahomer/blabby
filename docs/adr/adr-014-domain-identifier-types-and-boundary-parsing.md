# ADR-014: Domain identifier types and boundary parsing

- **Status:** Accepted
- **Date:** 2026-05-16
- **Related:** [ADR-011](adr-011-cross-boundary-pid-propagation.md), [ADR-012](adr-012-watch-based-connection-lifecycle.md), [ADR-013](adr-013-business-errors-as-response-values.md)

## Context

Two identifiers are load-bearing across every layer of this system: the authenticated user (`user_id`) and the chat room (`room_id`). Both flow through HTTP path captures, JWT subject claims, WebSocket auth frames, cluster proto requests, grain state, and structured log lines. Until now both have been represented as raw `string` everywhere.

Three problems compound at this representation:

**Mix-ups are caught only by code review.** The User grain's `JoinRoom` handler takes a proto request whose two relevant strings are `room_id` and (implicit, from grain identity) the user. The Room grain's `Join` handler takes a `user_id`. A typo that swaps the two compiles, runs, and produces a wrong-but-plausible result. The type system has nothing to say.

**Structural validation is duplicated and inconsistent.** `gateway.validateRoomID` trims, length-caps, and rejects control characters and Unicode whitespace inside the captured `{id}`. `connection.NewUserID` (the only existing value-object on this axis) trims and checks non-empty — but does not enforce length, control characters, or Unicode whitespace. The HTTP auth middleware accepts any non-blank JWT subject. The grain handlers re-check non-emptiness inline. Six call sites, three different rule sets, no canonical reference.

**One specific structural rule is missing.** Go 1.22+ mux URL-decodes `%2F` to `/` in path captures, so a crafted request can land a `room_id` like `foo/bar` at the User grain. The grain re-validates non-emptiness as defense-in-depth, but neither the gateway nor the grain rejects the slash. The room ultimately addressed at the cluster boundary is not the room the operator sees in the URL.

A related question that *isn't* the problem here, but worth naming so the decision below doesn't overreach: there are no storage-backed `Room` or `User` entities yet. Today every "room" is just an identifier; the description, creation time, member roster, and similar fields exist only on the cluster boundary's proto messages, not as a persistent shape. Any decision about identifier types must not preempt the eventual entity-type design.

## Decision

**Introduce a small `internal/ids` package containing two value-object types — `UserID` and `RoomID` — that share a uniform structural rule set, are distinct at the Go type level, and are parsed exactly once at each boundary where their underlying strings enter the system.**

Concretely:

- The package exports two opaque types:
  ```go
  type UserID struct{ value string }
  func NewUserID(raw string) (UserID, error)
  func (id UserID) String() string
  func (id UserID) LogValue() slog.Value

  type RoomID struct{ value string }
  func NewRoomID(raw string) (RoomID, error)
  func (id RoomID) String() string
  func (id RoomID) LogValue() slog.Value
  ```
- Both types implement [`slog.LogValuer`](https://pkg.go.dev/log/slog#LogValuer) so structured-log call sites pass the typed value directly:
  ```go
  slog.Info("room handler enter", "user_id", userID, "room_id", roomID)
  ```
  The JSON handler installed at process startup reaches a typed value as `KindAny` and falls through to `encoding/json`, which serialises by reflection over struct fields — an unexported field renders as `{}` and the identifier is lost. `encoding/json` does not honour `fmt.Stringer`; `LogValuer` is the slog-specific bridge that does, and returning `slog.StringValue(id.value)` skips the reflection-based fallback entirely.
- Both constructors share an unexported `parseIdentifier` helper that enforces a single rule set:
  1. Trim leading and trailing whitespace.
  2. After trim, non-empty.
  3. Length ≤ 256 bytes.
  4. No ASCII control characters (`< 0x20` or `== 0x7F`).
  5. No Unicode whitespace anywhere in the trimmed value.
  6. No `/` (the URL path delimiter).
- Each constructor wraps the helper's typed error (`ErrEmptyIdentifier`, `ErrIdentifierTooLong`, `ErrIdentifierInvalidChar`) with its own prefix (`user_id: …`, `room_id: …`) so log readers can distinguish causes without losing the structural classification. Callers map any failure to the same on-wire error code (`INVALID_REQUEST`); the distinction is for operators, not clients.
- The two types are structurally identical but compile-distinct. A function expecting `UserID` rejects `RoomID` at compile time, and vice versa. Map keys, struct fields, and function parameters all carry the typing through to internal state.
- Inside the system, raw `string` for an identifier appears only at four boundaries:
  1. **JWT verification.** `auth.Authenticator.ValidateToken` parses the JWT subject claim into `UserID` and returns it on `Claims.UserID`. A structurally invalid subject yields `ErrTokenInvalid` — the JWT itself is treated as malformed.
  2. **HTTP auth middleware.** `gateway.authMiddleware` receives the already-typed `Claims.UserID` and places it in the request context via `auth.ContextWithUserID(ctx, ids.UserID)`. `UserIDFromContext` returns the typed value.
  3. **HTTP path extraction.** Gateway handlers parse `r.PathValue("id")` into `RoomID`. Failure is `4001 INVALID_REQUEST`.
  4. **Grain handler entry.** Each grain handler parses incoming proto string fields (`req.GetUserId()`, `req.GetRoomId()`) into typed values at the top of the function. The grain re-validates because its contract is "any caller within the cluster," not "only the gateway." Failure is the existing `INVALID_REQUEST` business error.
- Proto wire types remain `string`. Crossing the cluster boundary on the way out, typed values are converted via `.String()`. The wire format is not part of this decision; the type discipline is purely an in-process concern.
- Internal state holds typed values. The User grain's joined-rooms set becomes `map[ids.RoomID]struct{}`; the Room grain's member set becomes `map[ids.UserID]struct{}`. Cross-keying becomes a compile error.
- The types prove only their structural invariants. They do *not* claim that a user or a room exists, that the caller is authorized, or that the identifier matches any external taxonomy. Deeper rules (membership, authorization, existence) belong to the grains and handlers that own them, not to the value object.

## Consequences

### Positive

- **Mix-ups become compile errors.** `func notifyRoom(uid UserID, rid RoomID)` cannot be called with the arguments swapped. The single most common identifier bug in handler code is eliminated by the type system.
- **One canonical rule set.** Tightening or extending the rules touches one file. The Go 1.22+ `%2F` exposure closes here, not at every handler.
- **Validation lives at boundaries, not at every internal call.** Once a function parameter is `UserID`, no callee re-validates. The "Parse, don't validate" pattern is enforced by the type system.
- **Logs gain typed cause classification.** A rejected request logs `reason=identifier_too_long` instead of a generic `invalid_request` collapse, without changing the on-wire error code.
- **Future entity types slot in without rename.** When `Room` (storage-backed entity) lands, it sits alongside `RoomID` (reference). The two are unambiguously distinct concepts and never collide on name. The package layout in `internal/ids` is reserved for identifiers, not entities — entities will own their own packages when they have a real consumer.

### Negative

- **Boilerplate at the proto wire.** Every proto-construction site writes `.String()` and every proto-receipt site re-parses. The cost is constant and concentrated at the cluster boundary; internal code remains typed.
- **Tightening rules retroactively rejects values that used to pass.** Any test fixture, JWT, or stored identifier that contained a control byte, Unicode whitespace, or `/` previously passed; under this rule set it does not. No live deployment exists today, so the practical cost is limited to test-fixture updates.
- **Grain handlers re-parse identifiers from incoming proto requests.** A request that goes through the gateway is parsed twice. The grain's contract is the cluster boundary — any future cluster caller (backfill, second gateway) inherits the same checks without the grain trusting the caller's discipline.

### Neutral

- **No effect on the cluster wire format.** Proto fields stay `string`; the cluster client signature is unchanged. The type discipline is a pure in-process property.
- **No effect on the error taxonomy.** All structural failures continue to map to `4001 INVALID_REQUEST` on the wire. The typed errors are for log clarity, not for clients.
- **No effect on the cluster identity binding.** `cluster.GetUserGrainGrainClient(c, userID.String())` continues to use a string identity; the typed value's `.String()` is the only conversion needed.
- **`internal/ids` imports `log/slog`.** A non-trivial choice for a value-object package, but slog is part of the standard library and `LogValuer` is the idiomatic way for a custom type to participate in structured logging. The import is one-way; nothing in `log/slog` depends back on `internal/ids`.

## Alternatives considered

### Per-package identifier types

Each consumer defines its own `UserID` / `RoomID` (the `connection.UserID` shape, repeated for the gateway and the grains). No shared package.

Rejected because this is the same duplication pattern the error-code constants already suffer (`codeRoomNotMember = 4101` declared once per package), and it loses the cross-boundary equality that map keys and typed function parameters need. A `connection.UserID` cannot be used as a key in a grain-side `map[gateway.UserID]struct{}` without a conversion that re-imports the validation question.

### Single generic `Identifier[T]` with phantom types

Use a generic value object parameterised by a phantom tag (`type Identifier[T any] struct{ value string }`; `type UserID = Identifier[userTag]`). Identical underlying mechanism, fewer file-level types.

Rejected as too clever for this codebase. Reading `ids.NewUserID(raw)` is immediate; reading `ids.NewIdentifier[ids.UserTag](raw)` requires unpacking the generic. The shared-parser-plus-two-types approach achieves the same compile-time distinction with substantially better local readability.

### Proto-typed wire identifiers (`message UserId { string value = 1; }`)

Replace proto `string` fields with a nested message wrapping a single string field. Encodes the identifier kind at the wire level.

Rejected because proto's nested-message shape gives no value-object semantics — it is a struct with a string field, with no language-level constructor or validation hook. The wire cost (extra tag bytes) is small but real; the conceptual cost (parallel hierarchy of "Id message types" mirroring the value-object types) is larger. The wire format is the wire format; the type discipline belongs in Go, not in proto.

### Per-type shape rules (UUID-shaped UserID, kebab-case RoomID)

Have `NewUserID` enforce UUID format (matching today's JWT authenticator) and `NewRoomID` enforce kebab-case (matching today's `defaultRooms` slice).

Rejected for now as policy masquerading as structure. A value object should prove what it knows; the structural rules above are facts about the wire and the URL. UUID-shape and kebab-case are *current* choices that may change (a future external IdP emits a non-UUID subject; a future room provisioning grain allows capitalised names). Tightening rules later is additive; loosening them is harder. The shared parser keeps the door open.

### Prefix conventions (`user-<body>`, `room-<body>`)

Self-identifying IDs at the wire level, in the style of Stripe (`cus_…`, `pi_…`) or Slack (`U…`, `C…`).

Rejected for the same reason — and additionally because adoption is a breaking change to existing surfaces: JWT subjects, URL paths, the `defaultRooms` slice, and every test fixture. The operational benefit (an opaque ID self-identifies its kind in logs) scales with the number of identifier kinds. Today there are two; the type system distinguishes them in code; the structured-log key (`user_id` vs `room_id`) distinguishes them at the operator's terminal. Revisit when a third or fourth identifier type lands.

### Rely on `fmt.Stringer` for log rendering

Implement `String() string` only and pass typed identifiers to `slog`, expecting the handler to render the string form.

Rejected because `encoding/json` (where the JSON handler falls through for `KindAny`) does not honour `fmt.Stringer`; the identifier is lost at every JSON log line. `slog.TextHandler` honours `fmt.Stringer` via `fmt.Sprint`, but the project ships JSON only. The marginal cost of the second method per type is worth not losing the identifier.

### Storage-backed `Room` and `User` aggregates instead of identifier types

Skip the identifier types and define `Room { ID, Description, CreatedAt, … }` and `User { ID, … }` directly. Pass aggregates around.

Rejected as premature. There is no persistence layer, no storage-backed shape, and no consumer that would benefit from receiving a `Room` aggregate (the join, leave, and post-message commands all care only about the identifier, not the room's description). When persistence lands, the aggregate types will join `RoomID` / `UserID` in the same shape this ADR sets up; there is no rename or migration to do.

## Scope of applicability

This ADR governs the representation of `user_id` and `room_id` across the codebase. It does **not** govern:

- **Storage-backed entities.** A future `Room` or `User` type with persistence-derived fields is out of scope; this ADR reserves the `internal/ids` package for identifiers only.
- **Cluster identity strings.** `cluster.GetUserGrainGrainClient(c, identity)` takes a `string` identity opaque to the cluster. The gateway converts `UserID` → `string` at this seam; the grain receives it via `ctx.Identity()` as a `string`. Whether `ctx.Identity()` should be re-parsed inside the grain is left to a future decision when a concrete reason to enforce it arises.
- **Other domain-significant strings.** Authentication tokens, message text, and similar values are not covered. `connection.AuthToken` already exists with its own value-object discipline and is unaffected.

## References

- [ADR-011](adr-011-cross-boundary-pid-propagation.md) — Cross-boundary PID propagation. Same general principle (parse once at the boundary, hold typed values internally) applied to actor identities; ADR-014 applies it to domain identifiers.
- [ADR-013](adr-013-business-errors-as-response-values.md) — Business errors as response values. Defines the `INVALID_REQUEST` error code that identifier-parse failures map to on the wire.
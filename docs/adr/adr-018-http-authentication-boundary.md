# ADR-018: HTTP authentication boundary and identity propagation

- **Status:** Accepted
- **Date:** 2026-06-13
- **Related:** [ADR-003](adr-003-websocket-authentication.md), [ADR-009](adr-009-error-response-format.md), [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md), [ADR-016](adr-016-gateway-backend-tier-separation.md)

## Context

Protected HTTP endpoints need three things wired together: token validation
(is this bearer token good?), an HTTP-shaped rejection (a 401 with the
project's error envelope), and identity propagation (the handler behind the
middleware must know *who* is calling). Each pulls in a different direction
when deciding where the code lives:

- Token validation is transport-agnostic — the WebSocket's first-message
  auth ([ADR-003](adr-003-websocket-authentication.md)) validates the very
  same tokens and must classify failures identically.
- The rejection is gateway-shaped — the error envelope, its codes, and
  `WriteErrorResponse` belong to `internal/gateway`
  ([ADR-009](adr-009-error-response-format.md)).
- Identity propagation tempts the path of least resistance —
  `context.Context` flows everywhere, so the authenticated user ID *could*
  ride it from the middleware all the way into grains and storage. Context as
  an identity bus is a recurring drift point: dependencies disappear from
  function signatures, and code deep in the stack grows an invisible
  requirement that some upstream middleware ran.

A naive placement — an HTTP middleware inside `internal/auth` — collides with
the first two forces at once: the middleware would need the gateway's
envelope to write its 401s, forcing `internal/auth` to import
`internal/gateway` and inverting the dependency direction (the gateway
consumes auth, not the reverse).

## Decision

**Token primitives live in `internal/auth` and are transport-agnostic. The
HTTP middleware lives in `internal/gateway` and owns the HTTP-shaped
rejection. Identity crosses exactly one layer via context, then travels as an
explicit typed argument.**

Three decisions, concretely:

1. **The middleware is gateway code.**
   `internal/gateway/middleware_auth.go` defines `authMiddleware` (and the
   `requireAuth` route helper). It extracts the bearer token per RFC 6750,
   validates it through the injected `Authenticator`, and writes failures
   with the gateway's own envelope — 1003 for a missing/malformed header,
   1002 for expiry, 1001 otherwise. Placing it here keeps the dependency
   arrow pointing one way: `gateway → auth`, never back.

2. **`internal/auth` owns the transport-agnostic vocabulary.** The
   `Authenticator` interface, the sentinel errors
   (`ErrTokenMissing` / `ErrTokenInvalid` / `ErrTokenExpired` in
   `internal/auth/errors.go`, with deliberately generic messages so they
   leak nothing if surfaced), and the user-ID context key with its accessors
   (`internal/auth/context.go`). Both transports consume the same
   vocabulary: the HTTP middleware branches on
   `errors.Is(err, auth.ErrTokenExpired)`, and the WebSocket's first-message
   auth (`internal/actor/connection/connection.go`) does exactly the same —
   one validation path, one failure taxonomy, two transports.

3. **`auth.UserIDFromContext` is a transport→handler bridge only.** The
   middleware calls `auth.ContextWithUserID` after validation; the handler
   directly behind it recovers the identity once. From there, identity is an
   explicit `id.UserID` argument
   ([ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md)) to
   everything deeper — the gateway's grain calls take it as a parameter
   (`internal/gateway/user_grain_caller.go`'s `callerFor(userID)`), and
   grains receive it in message fields. Layers that do not own a transport
   boundary never read it from a context. A failed lookup behind
   `requireAuth` is a wiring bug (a route registered without the
   middleware), not a runtime condition to handle.

The backend tier never participates: JWTs are issued and validated by
gateways alone, and identity crosses the tier boundary as a parsed user ID
in message payloads ([ADR-016](adr-016-gateway-backend-tier-separation.md)).

## Consequences

### Positive

- **The dependency graph stays acyclic and truthful.** `internal/auth` has
  no HTTP opinions and imports nothing from the gateway; it can serve any
  future transport (gRPC gateway, admin CLI) unchanged.
- **Two transports cannot drift apart.** HTTP and WebSocket auth share the
  interface, the sentinels, and therefore the failure classification —
  an expired token is 1002 on HTTP and the same taxonomy code on the
  socket, by construction.
- **Dependencies are visible at every signature below the bridge.** A
  function that needs the caller's identity says so in its parameter list.
  Tests construct an `id.UserID` and pass it — no context scaffolding, no
  middleware simulation.
- **Context misuse is structurally contained.** Exactly one writer
  (`ContextWithUserID` in the auth middleware) and one sanctioned reader
  layer (handlers directly behind it); the unexported key prevents foreign
  packages from minting lookalike values.

### Negative

- **A sliver of context-value indirection remains.** Inside the gateway,
  the middleware→handler handoff still rides `context.Context`, with its
  compile-time invisibility. Confining it to one hop is the mitigation, not
  a cure; the godoc on `UserIDFromContext` carries the contract.
- **Handlers repeat one line of ceremony.** Each protected handler opens by
  recovering the user ID from context. A handler-signature redesign could
  remove it; not worth diverging from `net/http` idiom today.
- **The middleware is coupled to the gateway by design** — reusing it
  outside `internal/gateway` would mean extracting the envelope first. That
  is the accepted price of decision 1.

### Neutral

- Login (`POST /login`) is unauthenticated by definition and sits outside
  this boundary; it consumes `Authenticator.Authenticate` directly rather
  than the middleware.

## Alternatives considered

### Middleware inside `internal/auth` with an injected error writer

Keep the middleware transport-agnostic by parameterizing the failure
response (a callback or interface the gateway supplies). Avoids the import
inversion in letter but not in spirit: the abstraction exists only to dodge
a dependency arrow, every consumer must wire the callback correctly for
classification to stay uniform, and the indirection obscures the one thing
a reader wants to know — what does a failure actually return? One concrete
middleware per transport, sharing the primitives underneath, is shorter and
plainer.

### Context-borne identity end to end

Let every layer read `UserIDFromContext`, grains included. Rejected:
identity becomes an ambient dependency — function signatures stop telling
the truth, actor-message boundaries (where contexts don't flow) need
smuggling anyway, and a missed middleware turns into a nil-identity bug deep
in the stack instead of a compile error at a call site.

### Per-handler token validation, no middleware

Each protected handler validates its own bearer token. Rejected: N copies
of extraction + classification + rejection that must never diverge, and the
route table stops showing which endpoints are protected —
`g.requireAuth(...)` at registration is self-documenting.

## References

- [ADR-003](adr-003-websocket-authentication.md) — the WebSocket
  counterpart consuming the same `internal/auth` vocabulary.
- [ADR-009](adr-009-error-response-format.md) — the envelope and codes the
  middleware's rejections use.
- [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md) — the
  typed `id.UserID` that identity travels as beyond the bridge.
- [ADR-016](adr-016-gateway-backend-tier-separation.md) — why tokens stop at
  the gateway tier entirely.
- `internal/gateway/middleware_auth.go` — the middleware and its
  classification table.
- `internal/auth/context.go` — the bridge, with the explicit-argument
  contract in its godoc.

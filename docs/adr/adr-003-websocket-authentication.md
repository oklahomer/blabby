# ADR-003: WebSocket authentication — why first-message auth over query parameter token

- **Status:** Accepted
- **Date:** 2026-06-13
- **Related:** [ADR-002](adr-002-client-protocol.md), [ADR-012](adr-012-watch-based-connection-lifecycle.md), [ADR-018](adr-018-http-authentication-boundary.md)

## Context

HTTP endpoints authenticate with `Authorization: Bearer <token>`
([ADR-018](adr-018-http-authentication-boundary.md)). The WebSocket endpoint
cannot simply copy that: the browser `WebSocket` API offers no way to set
arbitrary headers on the upgrade request. Any browser-reachable design has to
carry the token somewhere else, and the common answers each leak or contort:

- **Query parameter** (`/ws?token=…`): the URL — token included — lands in
  server access logs, reverse-proxy logs, and browser history. Tokens are
  bearer credentials; an access log becomes a credential store. This is the
  failure mode the project's no-credential-logging rule exists to prevent,
  and it would be enforced by *every log on the path*, most of which this
  project does not control.
- **Cookie:** works for browsers but imports cookie semantics — CSRF
  surface, SameSite tuning — and is foreign to the Phase 1 TUI client, which
  holds a token, not a cookie jar.
- **`Sec-WebSocket-Protocol` smuggling** (token as a subprotocol value):
  header-clean but bends protocol negotiation into a credential channel,
  surprising every intermediary and reader.

The remaining option moves auth *inside* the connection: upgrade first, then
require the first frame to authenticate. The cost is a window in which an
unauthenticated socket exists, which must be bounded.

## Decision

**The client authenticates with its first WebSocket frame. The token never
appears in the URL or the upgrade request. Unauthenticated connections are
closed on a deadline.**

- `GET /ws` upgrades without inspecting credentials and spawns the
  connection's actor (`internal/gateway/handler_ws.go`,
  [ADR-001](adr-001-grain-topology.md)).
- The first frame must be `{"type":"auth","token":"…"}`. The actor's pre-auth
  behavior validates the token with the same `Authenticator` the HTTP
  middleware uses (`internal/auth`), so HTTP and WebSocket accept exactly the
  same tokens and fail in the same vocabulary.
- On success the actor replies with an auth-success frame, registers with the
  user's User grain ([ADR-012](adr-012-watch-based-connection-lifecycle.md)),
  and switches to its post-auth behavior. On failure it sends a structured
  auth-error frame and closes.
- The deadline is enforced by a receiver middleware
  (`internal/actor/connection/auth_timeout.go`): on the actor's `Started`
  message it schedules a one-shot timeout message to self — 5 seconds by
  default (`defaultAuthTimeout` in `internal/actor/connection/connection.go`,
  overridable via the `WithAuthTimeout` option). If the timeout fires before
  authentication, the connection is closed with an auth error. The timer is
  never cancelled; the post-auth behavior simply ignores a late timeout
  message, which keeps the middleware a pure scheduler with no shared state.
- No other pre-auth frame makes progress. Malformed and unrecognized frames
  are logged and ignored — the client may still authenticate before the
  deadline — while an auth frame missing its token is rejected with an auth
  error and the connection closed. Nothing is ever processed on behalf of an
  unauthenticated peer.

## Consequences

### Positive

- **No token in any URL, ever.** Access logs, proxy logs, and history are
  clean by construction rather than by log-scrubbing discipline.
- **One auth implementation for both transports.** The `Authenticator`
  interface is the single validation path; WebSocket auth cannot drift from
  HTTP auth (same sentinels, same expiry handling).
- **Works identically for the TUI client and a future browser client** — both
  can send a first frame; neither needs header control on the upgrade.
- **The deadline doubles as a demonstration** of receiver middleware
  scheduling a self-message on `Started` — a reusable Proto.Actor pattern this
  codebase wants to teach.

### Negative

- **Resources are committed before identity is known.** Each upgrade spawns an
  actor and holds a socket for up to the deadline; an abusive peer can open
  sockets and let them idle. Bounded per-connection by the auth deadline
  (5 seconds by default), but unauthenticated connection *count* is not
  limited — rate limiting at the edge is the eventual answer.
- **A two-step handshake clients must implement correctly.** Connect-then-auth
  is more protocol than connect-with-header; the client must also handle the
  auth-error and timeout frames distinctly from transport failures.
- **The upgrade response cannot signal auth failure.** A client with a bad
  token sees a *successful* upgrade followed by an error frame and close,
  which is less conventional than a 401 at upgrade time.

### Neutral

- Origin checking is independent of this decision and currently permissive
  (the TUI client sends no `Origin`); it must tighten before any browser
  client ships — tracked in `internal/gateway/handler_ws.go`.

## Alternatives considered

### Token in the query string

Authenticate during upgrade via `/ws?token=…`. Rejected for the leak surface
described in Context: bearer credentials in URLs propagate into logs and
history that outlive the token's intended audience. Rejecting this option is
the origin of the whole design.

### `Authorization` header on the upgrade request

Clean and 401-capable — and unavailable to browser clients, whose WebSocket
API cannot set the header. Workable for the TUI alone, but it would fork the
auth story per client type and close the door this project wants open.

### Cookie-based upgrade auth

Browser-friendly and header-free, but imports CSRF/SameSite concerns,
requires the server to mint cookies alongside JWTs, and gives the non-browser
client the worst fit of all options.

## References

- [ADR-002](adr-002-client-protocol.md) — where the WebSocket sits in the
  client protocol.
- [ADR-012](adr-012-watch-based-connection-lifecycle.md) — what successful
  auth registers with the User grain.
- [ADR-018](adr-018-http-authentication-boundary.md) — the HTTP counterpart
  and the shared `internal/auth` primitives both transports consume.
- `internal/actor/connection/auth_timeout.go` — the deadline middleware,
  including the fire-unconditionally / ignore-if-late design.

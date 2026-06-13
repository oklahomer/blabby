# ADR-002: Client protocol — why HTTP for commands and queries, WebSocket for events, both JSON

- **Status:** Accepted
- **Date:** 2026-06-13
- **Related:** [ADR-003](adr-003-websocket-authentication.md), [ADR-009](adr-009-error-response-format.md), [ADR-015](adr-015-command-query-vs-notification.md), [ADR-016](adr-016-gateway-backend-tier-separation.md)

## Context

Chat traffic has two distinct shapes, and a client protocol has to serve
both:

- **Commands and queries** — log in, join a room, send a message, list
  rooms. The client initiates, needs a verdict (did it work? why not?), and
  proceeds based on the answer.
- **Events** — messages and membership changes produced by *other* people's
  actions (and echoes of your own). The server initiates; the client cannot
  poll its way to timeliness without waste.

Internally, everything is Protobuf over Proto.Actor's transport — the grain
protocol is generated from `proto/user/user.proto` and
`proto/room/room.proto`. Exposing that protocol to clients would couple every
client to the proto toolchain and to actor-level message shapes that exist
for the cluster's benefit, not the client's. A reference implementation also
wants the lowest possible barrier for someone writing a throwaway client to
poke at the system.

## Decision

**Clients speak two channels through the gateway: HTTP for commands and
queries, one WebSocket for server-pushed events. Both carry JSON. Protobuf
never crosses the client boundary.**

- **Commands use HTTP methods matching their client-facing semantics**:
  `POST /login` and `POST /rooms/{id}/messages`,
  `PUT /rooms/{id}/membership` to ensure membership, and
  `DELETE /rooms/{id}/membership` to ensure absence. **Queries are HTTP GET**
  (`GET /rooms`, which serves the gateway's static Phase 1 room
  catalogue without touching any grain, and `GET /rooms/joined`, a User-grain
  query). Handlers live in `internal/gateway/handler_room.go`,
  `handler_room_query.go`, and `handler.go` (login). Each request gets
  standard HTTP semantics: status codes, the JSON error envelope of
  [ADR-009](adr-009-error-response-format.md), and — everywhere except the
  token-issuing `POST /login`
  ([ADR-018](adr-018-http-authentication-boundary.md)) — bearer-token auth.
  The verdict is the response — a send that returns 200 has been accepted by
  the room.
- **Events arrive on `GET /ws`** (`internal/gateway/handler_ws.go`), a
  persistent WebSocket carrying JSON frames typed by a `type` field
  (`message`, `joined`, `left`, `error`, the auth handshake frames, and the
  `ping`/`pong` heartbeat pair behind the connection actor's
  `WithAppHeartbeat` option). Outbound frame encoding lives in
  `internal/actor/connection/encoder.go`, inbound decoding in `decoder.go`.
- **The gateway is the translation boundary** ([ADR-016](adr-016-gateway-backend-tier-separation.md)):
  JSON is parsed into Protobuf before anything enters the actor system, and
  grain responses are rendered back to JSON. JSON field names are
  `snake_case` throughout.

The split mirrors the internal command/notification distinction of
[ADR-015](adr-015-command-query-vs-notification.md): interactions whose
result the caller awaits ride request/response HTTP; announcements ride the
push channel. A client never learns about new messages from a command
response — even its own send is rendered when the echo arrives on the
WebSocket.

The membership resource is an adapter at the User-grain routing boundary;
the Room grain's internal `Join`/`Leave` protocol remains action-oriented.
The User grain treats `ROOM_ALREADY_MEMBER` from `Join` and
`ROOM_NOT_MEMBER` from `Leave` as confirmation of the requested state, applies
the corresponding local set operation, and returns success. Retrying an
ambiguous PUT or DELETE therefore reconciles the User and Room membership
views without changing Room-grain behavior.

## Consequences

### Positive

- **Any HTTP-capable environment is a client.** `curl` plus a WebSocket
  library is a complete toolkit; no codegen, no proto files, no custom
  framing. This is load-bearing for a codebase meant to be explored.
- **Commands get HTTP's mature semantics for free** — middleware, auth
  headers, status codes, request scoping, timeouts — instead of reinventing
  request/response correlation over a socket.
- **One socket per client session**, regardless of how many rooms the user is
  in. Fan-in happens server-side (the User grain), so the connection count is
  bounded by devices, not by room membership.
- **Internal protocol evolves freely.** Grain message shapes, routing, and
  actor topology can change without touching the client contract, because the
  only shared surface is JSON at the gateway.

### Negative

- **Two channels, two lifecycles.** Clients manage an HTTP client *and* a
  socket, including the awkward states between them (commands succeeding
  while the event stream is down). The TUI client's connection-status
  handling exists because of this.
- **JSON is weaker than the internal contract.** No schema enforcement at the
  wire level; drift between server encoding and client decoding surfaces at
  runtime. Machine-readable specs (OpenAPI for HTTP, AsyncAPI for the socket)
  are the planned mitigation; `api/` is reserved for them.
- **Double serialization at the gateway** (JSON ↔ Protobuf) costs CPU per
  request. Accepted: the gateway tier scales independently
  ([ADR-016](adr-016-gateway-backend-tier-separation.md)).

### Neutral

- Events are one-way by design; the only client→server WebSocket traffic is
  the auth handshake ([ADR-003](adr-003-websocket-authentication.md)) and
  heartbeat `pong` replies. Everything else a client wants to *do* is a
  command and belongs on HTTP.

## Alternatives considered

### Everything over the WebSocket

Run commands as socket frames with correlation IDs. Rejected: rebuilds
request/response — correlation, timeouts, per-command auth, error mapping —
inside a custom frame protocol, while abandoning the HTTP ecosystem
(middleware, load balancers, plain-curl debuggability). The socket would also
become a single point coupling command availability to event-stream health.

### gRPC / Protobuf to clients

Expose the internal protocol directly. Rejected: every client inherits the
proto toolchain and the internal message vocabulary, and the boundary between
"what clients may rely on" and "what the cluster needs" disappears —
internal refactors become client-breaking changes.

### Server-Sent Events for the push channel

SSE instead of WebSocket. Close call: SSE is simpler and proxies well. Rejected
because the event channel needs *some* client→server traffic — the
first-frame auth handshake of
[ADR-003](adr-003-websocket-authentication.md) (SSE would force the token
into the URL, the exact leak that ADR rejects) and heartbeat pongs.
WebSocket keeps those on the same connection.

## References

- [ADR-003](adr-003-websocket-authentication.md) — how the event channel
  authenticates.
- [ADR-009](adr-009-error-response-format.md) — the error half of the HTTP
  contract.
- [ADR-015](adr-015-command-query-vs-notification.md) — the internal analogue
  of the command/event split.
- [ADR-016](adr-016-gateway-backend-tier-separation.md) — the process
  boundary where JSON↔Protobuf translation happens.
- `internal/gateway/gateway.go` — route registration tying the endpoint set
  together.

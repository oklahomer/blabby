# ADR-001: Grain topology — why User and Room are grains, UserConnection is a regular actor

- **Status:** Accepted
- **Date:** 2026-06-13
- **Related:** [ADR-012](adr-012-watch-based-connection-lifecycle.md), [ADR-016](adr-016-gateway-backend-tier-separation.md), [ADR-017](adr-017-supervision-strategy.md)

## Context

Proto.Actor offers two kinds of actors, and every stateful component in this
system has to be assigned to one of them:

- A **grain** (virtual actor) has a stable, cluster-wide identity. Callers
  address it by name (`kind` + `id`); the cluster decides which member hosts
  it, activates it on first use, passivates it when idle, and re-activates it
  — possibly elsewhere — after a node failure. A grain's *identity* outlives
  any particular activation.
- A **regular actor** is spawned explicitly on a specific node and is
  addressed by the `*actor.PID` returned from `Spawn`. Its identity *is* that
  one spawned instance; when it stops, the PID is dead and nothing recreates
  it.

The system has three stateful components to place:

1. A **chat user**: addressed by user ID, owns the set of active connections
   and the set of joined rooms, must be reachable from any node.
2. A **chat room**: addressed by room ID, owns the member set and a recent
   message buffer, must be reachable from any node.
3. A **connection**: one live WebSocket. It exists because a socket was
   accepted, and it is meaningless without that socket.

The deciding question is what each component's identity is bound to. Users and
rooms are *domain identities* — `alice` and `general` exist independently of
any process, socket, or node. A connection is a *resource identity* — it is
exactly one accepted socket on exactly one process, and no other process could
ever take over that socket.

## Decision

**User and Room are grains. UserConnection is a regular actor spawned per
WebSocket upgrade.**

- The `User` grain (`internal/grain/user/user.go`) is keyed by user ID. It
  owns the user's connection-PID set and joined-rooms set
  (`internal/grain/user/state.go`) and acts as the user's single agent in the
  cluster.
- The `Room` grain (`internal/grain/room/room.go`) is keyed by room ID. It
  owns the member set and the bounded recent-message buffer
  (`internal/grain/room/state.go`).
- The `UserConnection` actor (`internal/actor/connection/connection.go`) is
  spawned by the gateway's WebSocket handler, one per upgraded socket
  (`internal/gateway/handler_ws.go`). It owns the socket, the authentication
  state of that session, and the read/write pumps. A disconnect stops the
  actor; a reconnect is a fresh spawn with a fresh PID, never a revival of the
  old one.

Grain activations rebuild on demand: when a grain passivates on its receive
timeout or its host node leaves, the next message addressed to it produces a
fresh activation. In Phase 1 (no persistence) that activation starts empty and
state is rebuilt by subsequent traffic — connections re-register, membership
is re-established by user commands.

## Consequences

### Positive

- **Location transparency where it pays.** Any gateway can issue a command for
  any user or room without knowing where the grain lives; the cluster routes
  it. No application-level registry maps IDs to nodes.
- **The connection actor's lifecycle is honest.** A socket cannot be
  "reactivated on another node", and the topology never pretends it can.
  Failure handling follows directly: a connection is stopped, never restarted
  ([ADR-017](adr-017-supervision-strategy.md)), and the client reconnects.
- **Clean tier separation falls out.** Because UserConnection is a regular
  actor, it can live on a gateway node that hosts no grains
  ([ADR-016](adr-016-gateway-backend-tier-separation.md)).
- **Each abstraction is exercised the way the framework intends** — grains for
  identity-keyed domain state, plain actors for resource-bound workers — which
  is the kind of example this codebase exists to provide.

### Negative

- **Grain state is hostage to activation lifecycle.** Passivation and node
  failure drop in-memory state, so every consumer must tolerate an
  empty-but-correct grain (see [ADR-010](adr-010-eventual-consistency-model.md)
  on reporting that state honestly). Persistence (Phase 2) narrows this, but
  the topology itself does not guarantee continuity.
- **Two addressing schemes coexist.** Grains are addressed by identity, the
  connection actor by PID. Crossing between them requires carrying PIDs in
  message payloads ([ADR-011](adr-011-cross-boundary-pid-propagation.md)),
  which is more machinery than a single uniform scheme would need.

### Neutral

- The grain interfaces are defined in Protobuf (`proto/user/user.proto`,
  `proto/room/room.proto`) and the clients are generated, so the
  grain-vs-actor split is also a typed-RPC vs raw-message split.

## Alternatives considered

### Model the connection as a grain

Give each connection a generated ID and let the cluster manage it. Rejected:
a connection grain could be placed or reactivated on a node that does not
hold the socket, which is meaningless. Every property grains add — placement
freedom, reactivation, identity beyond the instance — is wrong for a
socket-bound resource. The framework distinction exists precisely for this
case.

### Model users and rooms as regular actors

Spawn user/room actors on demand and track their PIDs in a registry actor or
shared map. Rejected: this hand-rolls what the cluster already provides —
placement, lookup, single-activation guarantees, failure-driven reactivation —
and the registry becomes a single point of contention and a consistency
problem of its own.

### One grain per (user, device) instead of connection actors

Fold connections into the grain layer by keying grains on a composite
identity. Rejected: it multiplies grain population by device count, still
needs the socket bound to one process, and loses the one-mailbox-per-user
serialization point that makes multi-device fan-out simple (the User grain
fans out to all of a user's connections from one place).

## References

- [ADR-011](adr-011-cross-boundary-pid-propagation.md) — how PID-addressed
  connection actors and identity-addressed grains interoperate across nodes.
- [ADR-012](adr-012-watch-based-connection-lifecycle.md) — how the User grain
  keeps its connection set honest given that connections are plain actors.
- [ADR-016](adr-016-gateway-backend-tier-separation.md) — which process hosts
  which half of this topology.
- [ADR-017](adr-017-supervision-strategy.md) — supervision policy derived from
  the same identity reasoning (rebuildable vs resource-bound actors).
- `internal/gateway/handler_ws.go` — the spawn site: one UserConnection per
  upgraded socket.

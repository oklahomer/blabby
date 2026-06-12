# ADR-004: Message routing through User grain — why User grain acts as the user's agent

- **Status:** Accepted
- **Date:** 2026-06-13
- **Related:** [ADR-001](adr-001-grain-topology.md), [ADR-005](adr-005-unconditional-fan-out.md), [ADR-015](adr-015-command-query-vs-notification.md)

## Context

Once the gateway has authenticated a request, something has to carry the
user's command into the actor system and carry room events back out to the
user's devices. The shortest path would be direct: the HTTP handler calls the
Room grain, and the Room grain delivers events straight to each member's
UserConnection actors.

Three forces argue against the shortest path:

- **User-level state needs a single owner.** The set of rooms a user has
  joined, and the set of connections a user currently holds, are per-user
  facts. If the gateway talks to Room grains directly, those facts either live
  in the gateway (per-process, wrong once more than one gateway exists) or are
  scattered across Room grains (each room knows its members, but nobody can
  answer "which rooms has alice joined?" without asking every room).
- **Multi-device delivery needs a single fan-in point.** A user may hold
  several connections at once. If rooms delivered to connections directly,
  every Room grain would have to track every member's connection set — state
  that changes on every connect/disconnect of every member, multiplied across
  every room they're in.
- **User-scoped policy needs a single enforcement point.** Per-user concerns —
  authorization context, and per-user rate limiting when it lands — want one
  place on the message path that sees all of a user's traffic, regardless of
  which room it targets.

## Decision

**Every command and every delivery routes through the user's own User grain.
The gateway never calls a Room grain, and a Room grain never targets a
connection directly for room-event traffic.**

Outbound (commands and queries):

- HTTP handlers dispatch through the `userGrainCaller` seam
  (`internal/gateway/user_grain_caller.go`), which resolves the generated
  User-grain client for the authenticated user ID. The gateway's four
  grain-facing operations — `JoinRoom`, `LeaveRoom`, `SendMessage`,
  `GetJoinedRooms` — are all User-grain RPCs.
- The User grain applies user-level rules (membership bookkeeping in
  `internal/grain/user/state.go`, validation) and forwards room-targeted
  commands to the Room grain through its own `roomClient` seam
  (`internal/grain/user/room_client.go`). Commands stay synchronous
  request/response end to end ([ADR-015](adr-015-command-query-vs-notification.md)).

Inbound (deliveries):

- The Room grain fans events out to each member's *User grain*
  ([ADR-005](adr-005-unconditional-fan-out.md)), not to connections. The User
  grain then forwards to every registered connection PID it currently holds
  (`internal/grain/user/state.go`'s connection set, kept honest per
  [ADR-012](adr-012-watch-based-connection-lifecycle.md)).

The User grain is therefore the user's *agent*: the one stable,
cluster-addressable representative through which everything about a user
flows.

## Consequences

### Positive

- **"Which rooms has alice joined?" is one RPC.** The joined-rooms query reads
  the User grain's own state; no scatter-gather across rooms.
- **Multi-device echo is free.** Because delivery converges on the User grain,
  forwarding to N devices is a local loop over the connection set — rooms are
  oblivious to device count, and a user's own message reaches their other
  devices without any extra machinery.
- **Room state stays small and stable.** A Room grain tracks member
  *identities* only — nothing about connections, which change far more often
  than membership.
- **One choke point for user-scoped policy.** Rate limiting, audit, or
  per-user middleware attach to the User grain kind once and see all of that
  user's traffic.

### Negative

- **Every command pays an extra hop** (gateway → User grain → Room grain)
  versus calling the room directly. Phase 1's latency budget absorbs this; the
  hop buys the single-owner properties above.
- **The User grain is on the critical path for everything its user does.** A
  slow or failed User-grain activation affects all of that user's rooms.
  Mitigated by the grain being lightweight (two in-memory sets) and rebuildable
  on demand.
- **Two grain kinds must stay in protocol agreement.** The User grain
  translates between gateway-facing RPCs and Room-grain RPCs, so changes to
  room operations touch both protos.

### Neutral

- The double hop exists only for commands. Deliveries traverse Room → User →
  connection by design, which is the same number of hops any fan-in scheme
  would need.

## Alternatives considered

### Gateway calls Room grains directly

The HTTP handler resolves a Room-grain client and issues join/leave/send
itself. Rejected: user-level state loses its owner. Joined-rooms tracking
would either sit in the gateway process (wrong with multiple gateways, lost
on restart) or require interrogating rooms. User-scoped policy would have to
be re-implemented per endpoint, and multi-device delivery would still need a
separate fan-in point — reintroducing the User grain in all but name.

### UserConnection actor as the user's agent

Let the connection actor own joined-rooms state and route commands. Rejected:
the connection is per-socket, so per-user state would fork across devices and
die with each disconnect. The agent must outlive any single connection, which
is exactly the grain/actor distinction drawn in
[ADR-001](adr-001-grain-topology.md).

### Rooms deliver directly to connection PIDs

Keep commands through the User grain but let rooms hold member connection
PIDs for delivery. Rejected: every member's connect/disconnect would have to
be broadcast to every room they're in to keep room-held PID sets honest —
turning the cheapest, most frequent event (connection churn) into cluster-wide
writes, and duplicating the liveness tracking that
[ADR-012](adr-012-watch-based-connection-lifecycle.md) already centralizes in
the User grain.

## References

- [ADR-001](adr-001-grain-topology.md) — why the agent is a grain and the
  connection is not.
- [ADR-005](adr-005-unconditional-fan-out.md) — the delivery half: rooms fan
  out to member User grains unconditionally.
- [ADR-015](adr-015-command-query-vs-notification.md) — the synchronous shape
  of the command path and the asynchronous shape of deliveries.
- `internal/gateway/user_grain_caller.go` — the gateway-side seam pinning
  "all grain calls go to the User grain".
- `internal/grain/user/room_client.go` — the User-grain-side seam for the
  onward hop into Room grains.

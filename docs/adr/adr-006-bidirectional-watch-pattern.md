# ADR-006: Bidirectional watch pattern — why both sides watch each other for self-healing

- **Status:** Proposed
- **Related:** [ADR-012](adr-012-watch-based-connection-lifecycle.md), [ADR-001](adr-001-grain-topology.md)

## Context

A UserConnection actor and its User grain hold references to each other for
the lifetime of a session: the grain holds the connection's PID so it can
forward deliveries; the connection holds the grain's identity so it can route
commands. Either side can die independently — the connection with its socket
or gateway process, the grain with its passivation timeout or its host node.
A reference to a dead counterpart is worse than no reference: it fails
silently, message by message.

One direction of this problem is solved and shipped.
[ADR-012](adr-012-watch-based-connection-lifecycle.md) has the User grain
`Watch` each registered connection and evict the PID on `*actor.Terminated`
— a connection's death, however it happens, heals the grain's view.

The reverse direction is not yet implemented, and the gap is observable:
when a User grain passivates or its node is lost, the connections that were
registered with it remain alive and unaware. The next *command* heals the
grain — any message addressed to it triggers reactivation, and the command
side recovers (the recovery path today is client-driven: a grain call times
out or fails, the socket closes, the client reconnects and re-registers). But
*deliveries* have no such trigger. A reactivated User grain starts with an
empty connection set ([ADR-010](adr-010-eventual-consistency-model.md)), so
room fan-outs addressed to that user vanish silently until the client happens
to reconnect. The user sits at a live socket receiving nothing, with no
signal that anything is wrong.

## Decision

**Make the watch bidirectional: in addition to the shipped
grain-watches-connection direction, the UserConnection actor watches its User
grain's activation and re-registers when the activation dies.**

The proposed reverse direction:

- On successful registration, the UserConnection obtains the PID of the User
  grain's current activation and calls `ctx.Watch` on it. (The registration
  response is the natural carrier for the activation PID, consistent with
  passing PIDs in payloads per
  [ADR-011](adr-011-cross-boundary-pid-propagation.md).)
- When `*actor.Terminated` for the grain's activation arrives, the connection
  re-issues `RegisterConnection`. Addressing the grain by identity reactivates
  it — possibly on another node — and the fresh activation learns about this
  connection immediately instead of at the client's next reconnect.
- Re-registration uses the existing idempotent register path; watch eviction
  on the grain side (ADR-012) is unchanged. Each direction heals
  independently, with no coordination between them.
- The silent-delivery-loss window shrinks from "until the client reconnects"
  to one death-watch propagation delay — the same bound ADR-012 already
  accepts in the other direction.

This ADR stays **Proposed** until that reverse direction ships; it is the
design intent the implemented half was built to compose with.

## Consequences

### Positive

- **Self-healing becomes symmetric.** Either party's death is detected by the
  survivor through the same primitive (`Watch`/`Terminated`), and each
  survivor knows exactly one repair action: evict (grain side) or re-register
  (connection side).
- **Deliveries recover without client involvement.** The user at a live
  socket keeps receiving room events across a grain reactivation, instead of
  silently missing everything until reconnect.
- **No new protocol surface.** Re-registration reuses the existing RPC; the
  only addition is carrying the activation PID in the registration response.

### Negative

- **Watching an activation, not an identity.** The grain-side PID names one
  activation; every reactivation requires a fresh watch. Getting this subtly
  wrong (watching a stale PID, racing a reactivation) produces exactly the
  silent gap the pattern is meant to close, so the implementation needs
  deliberate failover tests.
- **Re-registration storms are possible.** A node loss kills many activations
  at once; every affected connection re-registers near-simultaneously, and
  each re-registration reactivates a grain. Acceptable at Phase 1 scale, but
  the implementation should consider jitter if measurement shows thundering
  herds.
- **A second moving part per session.** Each connection carries watch state
  and a repair path that must be reasoned about alongside the auth and pump
  lifecycles it already manages.

### Neutral

- The pattern restores *registration*, not *state*. A reactivated grain's
  joined-rooms set is still empty until persistence lands
  ([ADR-007](adr-007-single-table-persistence.md)); honest emptiness
  ([ADR-010](adr-010-eventual-consistency-model.md)) is unchanged by who
  re-registers when.

## Alternatives considered

### Client-driven recovery only (the current state)

Rely on command failures to surface grain death: a grain call fails, the
connection closes, the client reconnects. Shipped today and acceptable for
Phase 1, but it converts a server-side, detectable event into user-visible
silence for delivery-only sessions (a user who is reading, not typing, has no
failing command to trip recovery). Kept as the fallback layer, not the
design.

### Periodic re-registration heartbeat

The connection re-registers every N seconds regardless of grain health.
Simple and watch-free, but it trades event-driven repair for a permanent
background load proportional to connection count, and the worst-case gap is
the full period rather than one death-watch delay.

### Grain persists its connection set

Have reactivation restore registrations from storage instead of asking
connections to re-register. Rejected: connection PIDs are exactly the state
that *must not* outlive a topology change — persisted PIDs may name dead
processes, recreating the stale-reference problem durably. Liveness state
belongs to the live actors that embody it.

## References

- [ADR-012](adr-012-watch-based-connection-lifecycle.md) — the implemented
  half: grain watches connection, evicts on Terminated.
- [ADR-011](adr-011-cross-boundary-pid-propagation.md) — the PID-in-payload
  mechanism the registration response would extend.
- [ADR-010](adr-010-eventual-consistency-model.md) — the empty-state honesty
  this pattern narrows but does not replace.
- `internal/grain/user/state.go` — the connection set that re-registration
  repopulates.

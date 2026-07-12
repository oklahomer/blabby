# ADR-006: Bidirectional watch pattern — why both sides watch each other for self-healing

- **Status:** Accepted
- **Date:** 2026-07-12
- **Related:** [ADR-012](adr-012-watch-based-connection-lifecycle.md), [ADR-001](adr-001-grain-topology.md)

## Context

A UserConnection actor and its User grain hold references to each other for
the lifetime of a session: the grain holds the connection's PID so it can
forward deliveries; the connection holds the grain's identity so it can route
commands. Either side can die independently — the connection with its socket
or gateway process, the grain with its passivation timeout or its host node.
A reference to a dead counterpart is worse than no reference: it fails
silently, message by message.

The two directions fail differently. A dead connection leaves the grain
fanning out to a socket that no longer exists. A dead grain activation is
subtler: commands are inherently self-healing, because any message addressed
to the grain's identity triggers reactivation — but a reactivated User grain
starts with an empty connection set
([ADR-010](adr-010-eventual-consistency-model.md)), and *deliveries* have no
trigger of their own. A user who is reading, not typing, generates no
failing call that would surface the problem: without a server-side repair,
that user sits at a live socket receiving nothing until the client happens
to reconnect.

## Decision

**The watch is bidirectional: each side watches the other through the same
primitive, and each survivor knows exactly one repair action.**

- The User grain `Watch`es every registered connection PID and evicts it on
  `*actor.Terminated` — a connection's death, however it happens, heals the
  grain's view. [ADR-012](adr-012-watch-based-connection-lifecycle.md)
  details this direction.
- In the reverse direction, the registration response carries the responding
  activation's own PID (`grain_pid`, populated from the grain's `ctx.Self()`
  — the registration response is the natural carrier, consistent with
  passing PIDs in payloads per
  [ADR-011](adr-011-cross-boundary-pid-propagation.md)), and the
  UserConnection calls `ctx.Watch` on it.
- When `*actor.Terminated` for the watched activation arrives, the connection
  re-issues `RegisterConnection`. Addressing the grain by identity reactivates
  it — possibly on another node — and the fresh activation learns about this
  connection immediately instead of at the client's next reconnect. Every new
  activation PID gets a fresh watch; a bounded number of failed re-register
  attempts closes the connection, so the client's reconnect remains the
  fallback layer beneath the self-healing one.
- Re-registration uses the existing idempotent register path; watch eviction
  on the grain side (ADR-012) is unchanged. Each direction heals
  independently, with no coordination between them.
- The silent-delivery-loss window is one death-watch propagation delay plus
  one re-register round trip, rather than "until the client happens to
  reconnect".

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
  silent gap the pattern closes, so deliberate failover tests guard the
  fresh-watch discipline.
- **Re-registration storms are possible.** A node loss kills many activations
  at once; every affected connection re-registers near-simultaneously, and
  each re-registration reactivates a grain. Acceptable at current scale;
  jitter is worth adding only if measurement shows thundering herds.
- **A second moving part per session.** Each connection carries watch state
  and a repair path that must be reasoned about alongside the auth and pump
  lifecycles it already manages.

### Neutral

- The pattern restores *registration*, not *state*. A reactivated grain
  hydrates its durable state from the store
  ([ADR-007](adr-007-database-authoritative-persistence.md)); this pattern only
  re-establishes the connection↔grain references, a concern orthogonal to state
  recovery ([ADR-010](adr-010-eventual-consistency-model.md)).

## Alternatives considered

### Client-driven recovery only

Rely on command failures to surface grain death: a grain call fails, the
connection closes, the client reconnects. Simple, but it converts a
server-side, detectable event into user-visible silence for delivery-only
sessions (a user who is reading, not typing, has no failing command to trip
recovery). Kept as the fallback layer — a connection that exhausts its
re-register attempts closes so the client reconnects — not the design.

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

- [ADR-012](adr-012-watch-based-connection-lifecycle.md) — the
  grain-watches-connection half: watch the registered PID, evict on
  Terminated.
- [ADR-011](adr-011-cross-boundary-pid-propagation.md) — the PID-in-payload
  mechanism the registration response extends.
- [ADR-010](adr-010-eventual-consistency-model.md) — the unpersisted-state
  honesty this pattern narrows but does not replace.
- `internal/grain/user/state.go` — the connection set that re-registration
  repopulates.

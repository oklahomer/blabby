# ADR-012: Watch-based connection lifecycle — no explicit Deregister, key by PID

- **Status:** Accepted
- **Date:** 2026-05-02
- **Related:** [ADR-011](adr-011-cross-boundary-pid-propagation.md), [ADR-006](adr-006-bidirectional-watch-pattern.md)

## Context

The User grain owns a set of `*actor.PID` values, one per active UserConnection actor, so it can fan messages out to every device a user is currently connected from. This set has to be kept honest: a PID whose actor has stopped must be removed before the next fan-out, otherwise every send dead-letters silently and the grain's logs lie about how many devices it actually reached.

Two design questions follow from that requirement:

1. **What identifies a connection in the grain's state?** A natural answer is "the PID itself" — protoactor PIDs are unique within a cluster and stable for the actor's lifetime. An alternative is a separate identifier (e.g. a UUID) generated at the boundary and threaded through to the grain alongside the PID.
2. **How does the grain learn that a connection has stopped?** Two options: an explicit Deregister RPC the connection actor invokes on its way out, or protoactor's `ctx.Watch(pid)` primitive, which delivers an `*actor.Terminated` system message to the watcher when the watched actor stops, regardless of cause — clean shutdown, panic, supervisor kill, node loss. `Watch` works across the cluster boundary via `EndpointWatcher`, so a User grain on one node can watch a UserConnection actor on another and still learn about that node's death.

The forces that shape the answer:

- **Cleanup must work for ungraceful exits, not just graceful ones.** A UserConnection that crashes, is killed, or whose host node disappears never gets to call a Deregister RPC. Any scheme that relies on cooperative cleanup leaks the dead PID until something else (next register? periodic sweep?) reclaims the slot.
- **The connection lifecycle is cleanly bounded by the actor's lifetime.** A WebSocket disconnect produces a fresh spawn, not a supervisor-restart of the existing actor. There is no scenario in which "the same connection" outlives its actor's PID — so a separate stable identifier solves a problem this system doesn't have.
- **The grain is not the connection's parent.** Without a parent-child relationship, lifecycle signals don't propagate automatically; the grain has to opt in to noticing.

## Decision

**The User grain keys its connection set by the registered PID, and eviction is driven by `ctx.Watch` + `*actor.Terminated`. No explicit Deregister RPC is exposed.**

Concretely:

- `RegisterConnection(requester_pid)` is the only connection-lifecycle RPC the User grain exposes. The request validates and reconstructs the PID per [ADR-011](adr-011-cross-boundary-pid-propagation.md), inserts it into the grain's connection map, and calls `ctx.Watch(pid)`.
- The map is `map[string]*actor.PID`, keyed by `Address + "/" + Id` of the registered PID. The key is composed from the public Address/Id fields directly — *not* `pid.String()`, which mutates an internal protobuf cache field on the PID and breaks `reflect.DeepEqual` against a freshly-constructed reference PID with the same Address/Id. The cache field is invisible to most code but surfaces in test assertions; composing the key from public fields avoids it without giving up uniqueness.
- The grain handles `*actor.Terminated` in `ReceiveDefault`. The handler removes the matching entry from the map. No client RPC is involved.
- The UserConnection actor still terminates itself on disconnect (closing the WebSocket cleanly is its own responsibility), but it makes no Deregister-style call on the way out. The grain learns about the stop through the watch.

The reverse direction — UserConnection watching the User grain to detect grain passivation or relocation — is **out of scope of this ADR**. [ADR-006](adr-006-bidirectional-watch-pattern.md) owns that decision. ADR-012 is unidirectional: grain watches connection.

## Consequences

### Positive

- **Crash and partition cleanup is load-bearing.** A UserConnection that panics, is killed, or whose host disappears does not leave a stale PID in the grain. The watch fires, the entry evicts, fan-out stops targeting the dead PID. The class of "ghost connection" failure mode is closed by construction.
- **One source of truth for liveness.** "Is this PID still alive?" is answered by the actor system, not by a hand-rolled Deregister contract that has to be called from every disconnect path. The UserConnection's lifecycle code shrinks accordingly — no carve-outs for "remember to deregister, but only if we're past auth."
- **Smaller wire format.** The connection-lifecycle surface is one RPC. Less proto, less generated code, less for a maintainer to track.
- **Idiomatic protoactor.** `Watch` + `Terminated` is the native primitive for tracking actor liveness in protoactor-go. Using it instead of an application-level Deregister protocol gets us closer to how protoactor users would expect this to work, which is one of the project's stated goals as a reference.

### Negative

- **Cleanup correctness depends on protoactor's death-watch propagating across the cluster.** `EndpointWatcher` is the supported mechanism, and we have an integration test (`TestUserGrain_SenderPID/WatchEvictsOnTermination`) that exercises eviction end-to-end through a real cluster, but the path is no longer entirely under our control. A bug in protoactor's cross-node watch would cause silent over-retention. Mitigation: the test asserts directly on eviction so a regression in this area fails CI rather than degrading silently.
- **Eviction is asynchronous.** `Terminated` arrives some bounded time after the actor stops. There is a window during which the User grain may attempt to fan out to a PID whose actor has already stopped; those sends dead-letter. The window matches the transport's death-detection latency and is the same window any actor-system-based liveness scheme has.
- **The PID is the tracing handle.** `Address/Id` is stable for the lifetime of one connection but less ergonomic for log grep than a UUID. The gateway can still mint a UUID at upgrade for client-side correlation; it just isn't needed across the cluster boundary.

### Neutral

- **Re-registration is a no-op rather than a replace.** Each UserConnection spawn produces a distinct PID, so "register the same connection again" doesn't arise in practice. If it did (the same actor calling Register twice), the second insert would find an existing entry for that PID and the second `ctx.Watch` would be a no-op at the protoactor level.

## Alternatives considered

### Explicit Deregister RPC

The grain exposes a `DeregisterConnection` RPC that the UserConnection actor calls on clean shutdown. Rejected: this only handles the cooperative-shutdown case. Crashes, kills, and node losses still need a fallback eviction path, and once that fallback is `Watch` + `Terminated`, the explicit RPC becomes redundant — the watch fires for clean shutdowns too. Two cleanup pathways means two places to keep in sync; one is simpler and harder to skip.

### Caller-supplied connection identifier

The grain accepts a `connection_id` (e.g. a UUID minted at WebSocket upgrade) and keys the map by it, with the PID as the value. Rejected: the identifier doesn't earn its place. The PID is already unique and stable for the actor's lifetime, and the only argument for a separate identifier — "stable name across actor restarts" — doesn't apply, because a disconnected WebSocket means a fresh spawn, not a supervisor-restart of the existing actor. Adding the identifier would be primitive obsession.

### Key by `pid.String()`

A natural-looking variant of PID-as-key. Rejected after observing that `pid.String()` mutates an internal protobuf cache field on the PID, which then breaks `reflect.DeepEqual` against a freshly-constructed reference PID with the same Address/Id. Composing the key from `Address + "/" + Id` avoids the cache mutation without giving up uniqueness.

## Scope of applicability

This ADR governs the User grain's connection set and the UserConnection actor's lifecycle. The same pattern is a reasonable default for any grain that needs to track outside actors, but each instance should derive the decision freshly — not every grain holds long-lived references to outside actors, and the cost/benefit of Watch versus an explicit protocol depends on the relationship.

## References

- [ADR-011](adr-011-cross-boundary-pid-propagation.md) — Cross-boundary PID propagation. The Register request still carries `requester_pid` and the grain still validates and reconstructs it; ADR-012 builds on that mechanism by treating the PID as the connection's identity.
- [ADR-006](adr-006-bidirectional-watch-pattern.md) — Bidirectional watch pattern. ADR-012 implements one half (grain watches connection); ADR-006 owns the reverse direction (connection watches grain).
- `internal/grain/user/sender_pid_test.go` — `TestUserGrain_SenderPID/WatchEvictsOnTermination` exercises eviction end-to-end through a real cluster: poison a registered actor, wait for Terminated propagation, assert that subsequent fan-out targets only the surviving connection.

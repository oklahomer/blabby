# ADR-011: Cross-boundary PID propagation — pass `*actor.PID` in the proto body, not `ctx.Sender()`

- **Status:** Accepted
- **Date:** 2026-04-30
- **Related:** [ADR-004](adr-004-message-routing-through-user-grain.md), [ADR-006](adr-006-bidirectional-watch-pattern.md)

## Context

Several places in this system need an actor outside a grain (typically a regular `actor.Actor` — the UserConnection actor is the first example) to register itself with a grain so the grain can later send messages back to it. The intuitive design is:

1. The outside actor invokes the generated grain client: `userpb.GetUserGrainGrainClient(c, userID).RegisterConnection(req)`.
2. Inside the grain, capture the calling actor's PID via `ctx.Sender()` and store it.
3. Later, fan messages out via `ctx.Send(storedPID, msg)`.

This design is broken. The mechanism:

- The generated grain client funnels every call through `cluster.Request` (see `cluster/default_context.go:98`), which delegates to `cluster.RequestFuture`. `RequestFuture` constructs an ephemeral `*actor.Future` and uses **the future's PID** as the message's `Sender`.
- `ctx.Sender()` inside the grain therefore returns the future's PID — not the calling actor's PID.
- The future PID lives only until the response is delivered. After that, sends to it dead-letter silently.

This is documented (with the protoactor source quoted) in [Oklahomer, "protoactor-go: Messaging Protocol", 2018](https://blog.oklahome.net/2018/09/protoactor-go-messaging-protocol.html), specifically the "RequestFuture() only has its future" section. The post recommends two fixes:

> "When the passing of sender actor's PID is vital for the recipient's task execution, use `Request()` or include the sender actor's `actor.PID` in the sending message so the recipient can refer to the sender actor for sure."

In our setting, `Request()` is not an option: the generated cluster grain client API has no equivalent that preserves the caller's PID across the cluster boundary; everything goes through `RequestFuture`.

We hit this in production-shaped form when the User grain first attempted to capture `ctx.Sender()` in `RegisterConnection`. An empirical regression test (`internal/grain/user/sender_pid_test.go`) spawned a real actor that registered itself via the cluster client and asserted whether a subsequent `ForwardMessage` reached it. Result: zero deliveries — the captured PID was indeed the future, and the fan-out dead-lettered.

## Decision

**At any actor → grain boundary where the grain needs to send messages back to the caller later, the caller's `*actor.PID` MUST be carried in the proto request body, not derived from `ctx.Sender()`.**

Concretely:

- The proto request includes two string fields: `pid_address` and `pid_id`.
- The caller sets these from `ctx.Self()`:
  ```go
  pid_address = ctx.Self().Address
  pid_id      = ctx.Self().Id
  ```
- The grain reconstructs the PID at the boundary:
  ```go
  pid := &actor.PID{Address: req.GetPidAddress(), Id: req.GetPidId()}
  ```
- The grain validates both fields non-empty at the boundary and rejects with `4001 INVALID_REQUEST` if either is missing (defense-in-depth — even though a real cluster caller never sends empties, the validation is the contract).
- The grain MUST NOT consult `ctx.Sender()` for caller identity in any handler that registers, watches, or routes back to an external actor.

Reading `ctx.Sender()` is acceptable only for short-lived in-handler operations (e.g., logging the future PID for diagnostics, or `ctx.Respond(...)` which targets the future by design and runs before the future expires).

## Consequences

### Positive

- **Eliminates an entire class of silent failures.** Without this rule, fan-outs from the grain to outside actors dead-letter. The failure is invisible: the grain logs success, the outside actor receives nothing, and the bug only manifests when a user notices their messages aren't arriving.
- **Stable across cluster reactivations.** When a grain passivates and re-activates, or when an outside actor's host node restarts, the next register call carries the new PID and the grain rebinds. The future-PID approach has no equivalent recovery story.
- **Wire-format clarity.** PIDs become explicit data, consistent with how every other identifying value (`user_id`, `room_id`, `connection_id`) crosses the cluster boundary in this project. No reliance on actor-system implicit context.
- **Testable from outside the actor system.** A unit test against a fake `cluster.GrainContext` can drive register-and-fan-out flows without spinning up real actors, because the PID is a plain proto field.

### Negative

- **Two additional proto fields per registration message.** Negligible cost; both are short strings (`pid_id` is typically a UUID-shaped suffix; `pid_address` is `host:port`).
- **Caller responsibility.** The outside actor MUST remember to set the fields. Mitigated by the grain rejecting empty values with a clear error code at the boundary.
- **Slight wire-format coupling to protoactor's PID shape.** If protoactor changes the PID structure (unlikely; `Address` + `Id` has been stable for years), the proto would need to grow alongside it.

### Neutral

- **No effect on grain-to-grain calls.** Grains address each other by `(kind, identity)` strings via the generated cluster client; no PID handling is involved on those paths.

## Scope of applicability

This rule applies whenever:

1. A non-grain actor (or any caller using the cluster client for a request-response RPC) needs to be addressable by a grain afterward, AND
2. The addressability must outlive the original request's response.

Concrete current applications:

- **User grain `RegisterConnection`:** UserConnection actors register their PID with the User grain so the grain can fan out `ForwardMessage` and `NotifyRoomEvent` to every active connection.
- **UserConnection actor (producer side):** When invoking `RegisterConnection`, MUST set `pid_address = ctx.Self().Address` and `pid_id = ctx.Self().Id`.

Likely future applications (re-derive when reaching them, but expect the same answer):

- **Grain reactivation on node failure:** Bidirectional watch — if a grain needs to watch an outside actor and react to its termination, the watch target's PID needs to be carried in the watch-establish message via the same mechanism.
- Any later feature where a regular actor subscribes to grain-emitted events.

## References

- [Oklahomer, "protoactor-go: Messaging Protocol", 2018](https://blog.oklahome.net/2018/09/protoactor-go-messaging-protocol.html) — the "RequestFuture() only has its future" section, with the protoactor source quoted.
- `internal/grain/user/sender_pid_test.go` — the regression test that empirically disproved the `ctx.Sender()` approach and now verifies the PID-in-payload design (single-device, multi-device, re-registration).
- `cluster/default_context.go:98` (protoactor-go `v0.0.0-20260118094027-288962e52f3f`) — `cluster.Request` always uses `RequestFuture` internally; this is what makes `ctx.Sender()` always be a future PID for cluster grain RPC.
- [ADR-004](adr-004-message-routing-through-user-grain.md) — Message routing through User grain; this ADR provides the PID-passing mechanics ADR-004 depends on.
- [ADR-006](adr-006-bidirectional-watch-pattern.md) — Bidirectional watch; the same mechanics will apply when watches need to target outside actors.

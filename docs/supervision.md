# Supervision: three flavors of failure handling

An actor that panics does not take the process down. Its supervisor catches
the failure and picks a directive: resume the actor, restart it, stop it, or
escalate. blabby exercises three supervision shapes, and the interesting
part is that each picks a *different* policy, for reasons the code spells
out. Official page:
[supervision](https://proto.actor/docs/ProtoActor/supervision).

The repo-wide rule underneath all three: no `recover()` inside `Receive`.
Panics are the supervisor's business; a hand-rolled recover would hide the
failure from the machinery built to classify and handle it
([ADR-017](adr/adr-017-supervision-strategy.md)).

## Guardian: a supervisor for root-spawned actors

`UserConnection` actors are spawned from the root context, which has no
parent to supervise them. `actor.WithGuardian` fills that hole
(`internal/actor/connection/supervision.go`), and its decider maps every
cause to **Stop**, with the reasoning in one comment worth reading whole:

- Restart is wrong because the actor's identity *is* the WebSocket; a fresh
  instance would inherit a dead socket. The client's reconnect, new socket
  and new actor, is the real restart.
- Resume is wrong because the session is an ordered protocol stream;
  skipping one message would silently corrupt it for the peer.
- Escalate is impossible at the root: protoactor guardians panic on
  `EscalateFailure`.

Two supporting details: the strategy value is shared across every spawn
because protoactor caches guardian processes per strategy instance, and the
supervisor only ever sees panics from the actor's own message handling. The
read/write pump goroutines recover their own panics and report them back as
ordinary messages, keeping goroutine failure and actor failure in one
lifecycle.

## Parent supervision: two children, two policies

Proto.Actor resolves a child's supervisor from its parent's props
(`actor.WithSupervisor`), and blabby's two grain children show opposite
policies chosen from the same question: *what would a restart mean here?*

The Room grain's fan-out worker **restarts, with an unlimited budget**
(`internal/grain/room/supervision.go`). The worker is long-lived and
stateless; a restart rebuilds the instance under the same PID and continues
with the remaining mailbox, losing only the job that panicked. The unlimited
budget is the subtle part: a capped strategy would eventually stop the
worker, leaving the grain's dispatcher holding a dead PID and silently
discarding every later fan-out for the rest of the activation.

The Maintenance grain's sweep worker **stops on any failure**
(`internal/grain/maintenance/maintenance.go`, `stopWorkerSupervisor`). The
worker is a one-shot: it consumes its single `runSweep` message and replies.
A restarted instance would sit idle forever, and since the grain never
passivates, idle corpses would accumulate. Stopping lets the grain's request
future time out and clear the in-flight flag instead.

## The logging decorator: add observability, not machinery

Both policies, and the connection guardian, are wrapped by one shared
decorator, `supervision.NewLoggingStrategy` (`internal/supervision`). It
logs a structured supervision line, then delegates directive application to
the Proto.Actor strategy it wraps (`actor.NewRestartingStrategy`,
`actor.NewOneForOneStrategy`), so restart bookkeeping and supervisor events
stay the runtime's job. Details worth stealing:

- The config pairs a pure decider (testable: cause in, directive out) with
  the applying strategy built from the same decider, so log and behavior
  cannot drift apart.
- Severity follows the directive: Resume and Restart log at Warn
  (recoverable), Stop and Escalate at Error (terminal).
- The panic value is reduced to a type name or error text before logging,
  so a panic carrying a payload never leaks it into the log stream.

## Try it

The supervision tests double as runnable demonstrations:

- `internal/actor/connection/failure_lifecycle_test.go` drives a panic
  through a live actor and asserts the guardian's Stop plus the
  `connection.supervision` log envelope.
- `internal/supervision/strategy_test.go` exercises the decorator against
  each directive.
- Grep the log stream of a running system for `.supervision` events; a
  healthy run has none, which is itself the observable claim.

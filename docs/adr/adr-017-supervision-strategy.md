# ADR-017: Supervision policy for non-grain actors

- **Status:** Accepted
- **Date:** 2026-06-08
- **Related:** [ADR-001](adr-001-grain-topology.md), [ADR-012](adr-012-watch-based-connection-lifecycle.md), [ADR-015](adr-015-command-query-vs-notification.md), [ADR-021](adr-021-scheduled-maintenance-jobs.md)

## Context

Proto.Actor requires a supervisor to decide what happens after an actor panics.
That decision follows one question: **can a new instance of this actor continue
useful work?** The answer depends on what gives the actor its identity, whether
it holds unrecoverable external state, and whether a rebuilt instance would still
have work to do.

The non-grain actors this system supervises answer that question differently:

- A **UserConnection** owns one WebSocket. The socket is the actor's identity
  and cannot be restored by constructing another instance — a rebuilt actor has
  no live socket to serve.
- A Room grain's **fan-out worker** is stateless. A new instance can continue
  processing later jobs from the same mailbox, so it is rebuildable *and* still
  useful after a restart.
- A maintenance grain's **sweep worker** is rebuildable but one-shot: it exists
  to run a single sweep and reply. A restarted instance has already consumed its
  one job and would sit idle — so, unlike the fan-out worker, a rebuild buys
  nothing.

Expected fan-out delivery failures are ordinary `error` values returned by the
User grain client. They are not actor failures: the worker logs the failed
recipient and continues its best-effort delivery loop.

## Decision

**Use actor supervision only for panics, with a policy based on whether a
rebuilt instance can continue useful work. Keep expected operational failures on
normal error paths.**

Each supervised actor supplies a pure Proto.Actor decider, structured log
attributes, and one of Proto.Actor's built-in strategies to a shared logging
decorator. The decorator records the failed actor or grain identity, message
type, panic text, and selected directive without logging the message body,
then hands the failure to the built-in strategy. Directive application,
restart bookkeeping, and supervision-event publication therefore stay in the
runtime; the shared code adds only the log line.

The decorator supports Proto.Actor's Resume, Restart, Stop, and Escalate
directives, but each actor policy uses only the directive appropriate to its
lifecycle.

### Stop UserConnection actors

Every UserConnection panic maps to **Stop**. Restart cannot repair or replace
the WebSocket owned by the failed actor, Resume could leave an ordered protocol
session in an unknown state, and a root guardian cannot escalate further. The
client reconnects with a new socket and a new actor.

Outbound backpressure is the anticipated panic on the actor message path. It
also maps to Stop, allowing the normal stopping lifecycle to close the
connection. Panics in read and write pump goroutines are recovered in those
goroutines and reported to the actor as transport-failure messages, so they do
not reach this supervisor.

### Restart the Room fan-out worker

Every unexpected fan-out worker panic maps to **Restart**, applied by
Proto.Actor's restarting strategy. The worker has no mutable domain state or
external-resource identity, so Proto.Actor can replace the actor instance
while preserving its PID and remaining mailbox.

The restart budget is deliberately unlimited. A capped strategy stops the
child once the budget is exhausted, which would leave the grain's dispatcher
holding a dead PID and silently discard every later fan-out job for the rest
of the activation — a worse failure mode than restart churn.

Restart does **not** retry the message that caused the panic. That message is
dropped. After the actor instance is rebuilt, processing continues with the
next queued message. This matches best-effort fan-out: an unexpected failure
may lose the in-flight job, but it does not permanently disable later fan-out.

The worker is spawned once during Room grain initialization. The dispatcher
retains that stable PID, avoiding a stop-and-respawn interval in which messages
could be sent to a terminating child.

Expected per-recipient errors do not cause a restart. `runNotify` and
`runForward` log them and continue with the remaining recipients.

### Stop the maintenance sweep worker

The maintenance grain's sweep worker maps to **Stop**, applied by a one-for-one
stop strategy. This is the same directive as UserConnection but for a different
reason: the worker is rebuildable, yet a rebuilt instance would have no work to
do. It is spawned per trigger to run one sweep and reply
([ADR-021](adr-021-scheduled-maintenance-jobs.md)); a restarted instance has
already consumed its single job, and because the maintenance grain never
passivates, such idle workers would accumulate for the process's lifetime.

Stopping is also what keeps the job self-healing. The grain tracks the sweep
with a request future whose reentrant continuation clears the running flag on
reply, failure, *or* timeout. Letting a panicked worker stop lets that future
resolve (with an error) and clear the flag, so a crashed sweep can never wedge
the job — whereas a restarted-but-idle worker would leave the future waiting.

## Consequences

### Positive

- Supervision policy matches each actor's actual lifecycle.
- Expected notifier errors remain explicit Go error handling rather than being
  converted into panics.
- The fan-out child keeps a stable PID across restarts, so later queued jobs are
  not routed through a named-child recreation race.
- Structured supervision logs share one implementation without sharing domain
  failure taxonomies, and directive application reuses Proto.Actor's built-in
  strategies instead of duplicating them.
- Required strategy collaborators are checked when the strategy is built,
  rather than causing a nil-function panic during failure handling.

### Negative / trade-offs

- The fan-out job that panics is lost and is not retried.
- Repeated panics from separate queued jobs can cause repeated actor restarts;
  operational monitoring must surface that pattern.
- The runtime emits its own recovery log in addition to the structured
  supervision event.

### Neutral

- Grain activations remain governed by the cluster runtime. The custom
  strategies govern only the non-grain children — the Room grain's fan-out
  worker and the maintenance grain's sweep worker.
- Resume and Escalate remain available through the decorator but are not part
  of the current connection or Room worker policies.

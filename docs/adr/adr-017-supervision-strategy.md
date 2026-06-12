# ADR-017: Supervision policy for connections and fan-out workers

- **Status:** Accepted
- **Date:** 2026-06-08
- **Related:** [ADR-001](adr-001-grain-topology.md), [ADR-012](adr-012-watch-based-connection-lifecycle.md), [ADR-015](adr-015-command-query-vs-notification.md)

## Context

Proto.Actor requires a supervisor to decide what happens after an actor panics.
That decision depends on what gives the actor its identity and whether a new
actor instance can continue useful work.

This system supervises two non-grain actors with different lifecycle needs:

- A **UserConnection** owns one WebSocket. The socket is the actor's identity
  and cannot be restored by constructing another actor instance.
- A Room grain's **fan-out worker** is stateless. A new instance can continue
  processing later jobs from the same mailbox.

Expected fan-out delivery failures are ordinary `error` values returned by the
User grain client. They are not actor failures: the worker logs the failed
recipient and continues its best-effort delivery loop.

## Decision

**Use actor supervision only for panics, with a policy based on whether the
actor can be rebuilt. Keep expected operational failures on normal error
paths.**

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

- Grain activations remain governed by the cluster runtime. The custom Room
  strategy governs the fan-out child only.
- Resume and Escalate remain available through the decorator but are not part
  of the current connection or Room worker policies.

# Observability: logs, dead letters, and metrics

Everything the actor system does is observable from outside: middleware
logs every dispatch, undeliverable messages surface as dead letters, and
the runtime's built-in instrumentation exposes counters and histograms.
This tour follows those layers from the inside out.

## Logging middleware and event names

Receiver middleware wraps every grain and connection dispatch
(`internal/middleware/logging.go`), and its package doc states the two
contracts worth knowing:

- **Emission order.** Lifecycle events log *before* the handler runs, so a
  slow shutdown is still attributed to the right actor; ordinary messages
  log *after*, so a panic during dispatch is visible as a missing line
  rather than a misleading present one.
- **No payloads, ever.** Message identity is the Go type name (`%T` with
  the pointer star trimmed), never serialized fields, so credentials and
  message text cannot leak through the logging path.

Event names follow one convention across the codebase: dotted, past-tense
action verbs (`grain.activated`, `connection.authenticated`,
`server.cluster.member_joined`). Middleware order is a stated rule too:
logging installs last, so the stream records the message the actor actually
receives, including synthetic ones earlier middleware produced. Official
page: [middleware](https://proto.actor/docs/ProtoActor/middleware).

## One JSON stream: the logger factory

Proto.Actor has its own internal logger, and `clusterboot.Build` routes it
through the process slog at Warn and above
(`protoActorLogger` in `internal/clusterboot/bootstrap.go`, floor applied
by `logging.NewMinLevelHandler`). Two effects, one of them a security
control: the framework's warnings and errors join blabby's structured JSON
stream, and the framework's built-in dead-letter line, which logs whole
message bodies at Info, stays out of it. That is the same leak class the
config's `RequestLog` default avoids (the
[cluster-bootstrap tour](cluster-bootstrap.md) covers that one). Official
page: [logging](https://proto.actor/docs/ProtoActor/logging).

## Dead letters as an operator signal

A message sent to a PID that no longer exists becomes a dead letter — the
actor model's "no guaranteed delivery" made concrete. A healthy system
still produces some: a fan-out racing a connection's death, a Stop or
Watch reaching an actor that just terminated. blabby subscribes to them on
the system's EventStream (`internal/clusterboot/deadletter.go`) and logs
`server.deadletter.observed` at Warn with safe fields only: target PID,
message *type name*, sender. Never the body.

The line is rate-limited with Proto.Actor's own `actor.NewThrottle`, whose
valve mechanics hide a teachable off-by-one: the valve returns `Closing` on
the count-th event, so a budget of three logs at most two lines per window,
with the window-end callback reporting the drops
(`server.deadletter.throttled`). Cluster topology changes ride the same
pattern (`topology.go`): subscribe before `StartMember`/`StartClient`,
because the first topology publishes while the member starts. Official
pages: [deadletter](https://proto.actor/docs/ProtoActor/deadletter),
[eventstream](https://proto.actor/docs/ProtoActor/eventstream),
[durability](https://proto.actor/docs/ProtoActor/durability).

## Metrics through an owned MeterProvider

protoactor-go ships OpenTelemetry instrumentation: actor spawn, stop,
restart, and failure counters, mailbox-length and receive-duration
histograms, dead-letter and futures counters, plus cluster-level metrics.
blabby exposes them per binary as a Prometheus scrape endpoint, opt-in:
`--metrics` serves `GET /metrics` on the gateway's internal listener,
`--metrics-listen` opens a dedicated operational listener on the backend.
Official page: [metrics](https://proto.actor/docs/ProtoActor/metrics).

The wiring (`internal/telemetry`, `clusterboot.Telemetry`) demonstrates two
lessons [ADR-022](adr/adr-022-protoactor-metrics-exposure.md) records:

- **Enabling takes both fields.** The metrics extension activates only when
  the actor system config carries a `MeterProvider` *and*
  `MetricsEnabled = true`; a provider alone is a silent no-op with an empty
  scrape. `TestBuildMetricsToggle` pins the wiring.
- **Own the provider.** blabby backs it with a dedicated Prometheus
  registry instead of the framework's one-call convenience, whose
  process-global side effects break any process running more than one actor
  system — which the in-process cluster tests do.

## Try it

- Start the backend with `--metrics-listen 127.0.0.1:9464`, chat for a
  minute, then `curl -s localhost:9464/metrics | grep '^protoactor'`:
  spawn counters, mailbox and receive-duration histograms, futures
  counters, all without a line of bespoke measurement code.
- Watch any log stream and read the envelope fields on `grain.msg` and
  `connection.msg` lines: type names only, joined into a per-grain trail by
  `grain_id`.
- Kill a client mid-conversation while another user keeps sending: if a
  fan-out races the watch eviction, `server.deadletter.observed` catches
  the undeliverable delivery, type name and PID, no payload.

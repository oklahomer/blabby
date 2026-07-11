# ADR-022: Proto.Actor metrics — an owned MeterProvider on a dedicated registry, exposed per binary

- **Status:** Accepted
- **Date:** 2026-07-11
- **Related:** [ADR-016](adr-016-gateway-backend-tier-separation.md), [ADR-017](adr-017-supervision-strategy.md), [ADR-021](adr-021-scheduled-maintenance-jobs.md)

## Context

Proto.Actor ships built-in OpenTelemetry instrumentation: actor spawn, stop,
restart, and failure counters, mailbox-length and receive-duration histograms, a
dead-letter counter, futures counters, and cluster/remote-level gauges such as
member count. Turning it on gives an operator visibility into actor and cluster
health with no bespoke measurement code. blabby wants that signal exposed as a
Prometheus scrape endpoint on each binary, opt-in per binary, with the
zero-configuration behavior unchanged.

Two facts shape how the endpoint is exposed. First, the two binaries have
different HTTP shapes ([ADR-016](adr-016-gateway-backend-tier-separation.md)): the
gateway already runs a public API listener and a separate internal listener for
operational endpoints ([ADR-021](adr-021-scheduled-maintenance-jobs.md)), while
the backend runs no HTTP server at all. Second, Proto.Actor's own metrics
extension activates only when the actor system's `Config` carries **both** a
`MetricsProvider` and `MetricsEnabled = true`; a provider without the flag yields
a no-op extension and an empty scrape, and no public config option sets the flag.

Proto.Actor offers a one-call convenience, `WithDefaultPrometheusProvider`, but it
carries process-global side effects that are incompatible with a codebase whose
tests stand up several actor systems in one process.

## Decision

**Enable Proto.Actor's OpenTelemetry metrics through a `MeterProvider` blabby
owns — backed by a dedicated Prometheus registry — and expose it per binary as a
Prometheus scrape endpoint, opt-in behind a flag.** The provider bundle lives in
`internal/telemetry`; the actor system wiring lives in `internal/clusterboot`; the
per-binary exposure lives in each `cmd`.

### Enabling takes both a provider and the flag, on one construction path

`clusterboot.Build` builds the actor system from a single `actor.Config`
(`actor.NewConfig()` then `actor.NewActorSystemWithConfig`). `Build` takes one
value parameter, `Telemetry{ MeterProvider }`, whose zero value leaves metrics
disabled. When a provider is supplied, `Build` sets *both*
`MetricsProvider` and `MetricsEnabled = true`; when it is not, both stay unset.

Routing Proto.Actor's own logger through blabby's JSON stream is not optional on
either path: the same config always installs the `LoggerFactory` that raises
Proto.Actor's logging to Warn and above, so its dead-letter subscriber — which
logs whole message bodies at Info — never leaks message text or bearer tokens into
the log stream. Keeping metrics and logging on one construction path means that
guarantee cannot be lost by enabling metrics.

### A dedicated registry, never the process-global default registerer

`internal/telemetry` creates its own `prometheus.NewRegistry()`, bridges an
OpenTelemetry Prometheus exporter onto it, and wires that exporter as the reader
of an SDK `MeterProvider`. The scrape handler is `promhttp.HandlerFor` over that
same registry. The Go runtime and process collectors are registered on the
dedicated registry too — free host metrics, safe precisely because the registry is
private to the bundle. Because nothing touches the global default registerer,
several bundles coexist in one process without a duplicate-registration panic, and
one binary's instruments never leak into another's scrape.

Enabling metrics does set one process-global: Proto.Actor calls
`otel.SetMeterProvider` on the supplied provider. That is correct for one actor
system per process, which is exactly what each binary runs; tests that enable
metrics account for the shared OpenTelemetry global rather than assuming
isolation.

### Why `WithDefaultPrometheusProvider` is unsuitable

The convenience helper is rejected for four concrete reasons: it registers its
collector into the process-global default Prometheus registerer; it calls
`http.Handle("/", …)` on the global `DefaultServeMux`, which panics if reached
twice in one process — blabby's in-process cluster tests build several actor
systems; it starts an unmanaged `ListenAndServe` goroutine whose error is
swallowed and whose lifecycle no caller can drain; and it returns a nil provider
on exporter construction error, silently disabling metrics. An explicitly owned
provider on a dedicated registry avoids all four and gives `Build`'s caller an
`http.Handler` to mount and a `Shutdown` to defer.

### Exposure differs per binary because their HTTP shapes differ

- **Gateway (`--metrics`, default off).** When set, the built handler is mounted
  as `GET /metrics` on the *existing* internal listener, which already defaults to
  loopback and is documented as network-restricted
  ([ADR-021](adr-021-scheduled-maintenance-jobs.md)) — the right home for an
  unauthenticated operational endpoint. The route is registered only when the
  handler is present, so with the flag off `GET /metrics` is a plain 404, and a
  wrong method gets the standard 405 companion. Nothing is ever mounted on the
  public API listener.
- **Backend (`--metrics-listen host:port`, default empty = off).** The backend has
  no HTTP server, so an enabled metrics endpoint runs on its own small listener
  with a `ReadHeaderTimeout`, serving `GET /metrics` plus a 405 and a 404
  catch-all. It starts after `StartMember` so instrumentation is live before the
  endpoint is scrapeable, and it drains via `Shutdown` on the signal path. A bind
  failure on the explicitly requested port is fatal, mirroring the gateway's
  servers. The flag's help text names it an operational endpoint to keep
  network-restricted.

In both binaries the `MeterProvider` is flushed with a bounded `Shutdown` on exit,
logging a non-nil error at Warn rather than dropping it.

## Consequences

### Positive

- **Actor and cluster health are observable with no bespoke measurement code.**
  Proto.Actor's built-in instruments plus the Go runtime collectors cover spawn,
  restart, failure, mailbox depth, dead letters, and member count out of the box.
- **Several actor systems coexist in one process.** The dedicated registry is what
  makes the in-process cluster tests — and any future multi-system tooling —
  immune to the default-registerer panics the convenience helper would cause.
- **Enabling metrics cannot silently do nothing or reopen the log leak.** The
  single construction path sets the provider and the enable flag together and
  always installs the log-level floor, so a half-configured system is not
  representable through `Build`.
- **Exposure fits each tier.** The gateway reuses its network-restricted internal
  listener; the backend gets a purpose-built operational listener; the public API
  surface is untouched.

### Negative / trade-offs

- **The endpoints are unauthenticated and rely on network isolation.** Like the
  scheduled-job trigger, both metrics endpoints assume their listener is not
  publicly reachable; exposure is a deployment responsibility.
- **Metrics enable a process-global OpenTelemetry meter provider.** One system per
  process makes this correct, but it means a test that turns metrics on shares that
  global and cannot run in parallel with another that does.

### Neutral

- **The feature is opt-in and off by default.** With no flag, neither binary opens
  a listener, sets a provider, or changes any prior behavior.
- **Scope is Proto.Actor's built-ins plus host metrics.** Tracing, log exporters,
  custom application metrics, dashboards, and alerting are deliberately out of
  scope; histogram boundaries stay at the SDK defaults.

## Alternatives considered

### Proto.Actor's `WithDefaultPrometheusProvider`

Use the framework's one-call helper. Rejected for the four side effects above —
global registerer, a handler on the global mux that panics on a second system, an
undrainable swallowed-error server goroutine, and a nil provider on error. Owning
the provider costs a small package and returns explicit control of the registry,
handler, and shutdown.

### Exposing metrics on the gateway's public listener

Serve `/metrics` from the public API mux to avoid a second listener. Rejected:
the metrics endpoint is unauthenticated operational data and belongs with the
other internal endpoints on the network-restricted listener, not on the
client-facing surface.

### A shared or embedded endpoint for the backend

Fold the backend's metrics into an existing server. Rejected because the backend
has no HTTP server by design ([ADR-016](adr-016-gateway-backend-tier-separation.md));
a small dedicated listener, opened only when requested, keeps the grain tier
HTTP-free unless an operator opts in.

## References

- [ADR-016](adr-016-gateway-backend-tier-separation.md) — the gateway is a cluster
  client with two listeners; the backend is a cluster member with none.
- [ADR-017](adr-017-supervision-strategy.md) — the actor lifecycle the counters
  measure (spawn, restart, stop, failure).
- [ADR-021](adr-021-scheduled-maintenance-jobs.md) — the gateway's internal
  listener, the network-restricted home the gateway's `/metrics` reuses.
- `internal/telemetry/prometheus.go` — the dedicated-registry provider, collectors,
  scrape handler, and shutdown.
- `internal/clusterboot/bootstrap.go` — the `Telemetry` parameter and the single
  actor-system construction path that sets the provider, the enable flag, and the
  logger factory.
- `cmd/gateway/main.go` and `cmd/backend/main.go` — the `--metrics` and
  `--metrics-listen` flags and the per-binary exposure.

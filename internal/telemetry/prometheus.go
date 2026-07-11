// Package telemetry owns blabby's metrics plumbing: an OpenTelemetry
// MeterProvider bridged onto a dedicated Prometheus registry, exposed as a
// standard scrape handler. It deliberately never touches the process-global
// default Prometheus registerer, so several instances can coexist in one
// process (the in-process cluster tests build many actor systems). The
// rationale is recorded in ADR-022.
package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// PrometheusMetrics bundles an OpenTelemetry MeterProvider with the dedicated
// Prometheus registry that backs it and the scrape handler for that registry.
type PrometheusMetrics struct {
	provider *sdkmetric.MeterProvider
	registry *prometheus.Registry
}

// NewPrometheusMetrics builds an OpenTelemetry MeterProvider backed by a
// dedicated Prometheus registry (never the process-global default registerer),
// registers the free Go runtime and process collectors on it, and returns the
// bundle carrying the scrape handler for that registry. The dedicated-registry
// design — and why WithDefaultPrometheusProvider is unsuitable — is documented
// in ADR-022.
func NewPrometheusMetrics() (*PrometheusMetrics, error) {
	registry := prometheus.NewRegistry()

	// Go runtime and process metrics come for free and are safe here precisely
	// because the registry is dedicated: registering these on the process-global
	// default registerer twice in one process would fail.
	if err := registry.Register(collectors.NewGoCollector()); err != nil {
		return nil, fmt.Errorf("register go collector: %w", err)
	}
	if err := registry.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, fmt.Errorf("register process collector: %w", err)
	}

	// The OTel Prometheus exporter is itself an SDK metric Reader; wiring it as
	// the MeterProvider's reader bridges every OTel instrument onto the registry.
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("build prometheus exporter: %w", err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))

	return &PrometheusMetrics{provider: provider, registry: registry}, nil
}

// MeterProvider returns the OpenTelemetry MeterProvider whose instruments are
// exported through HTTPHandler. It is handed to proto.actor via
// clusterboot.Telemetry so the framework's built-in instrumentation lands on
// this registry.
func (m *PrometheusMetrics) MeterProvider() metric.MeterProvider {
	return m.provider
}

// HTTPHandler returns the Prometheus scrape handler for the dedicated registry.
func (m *PrometheusMetrics) HTTPHandler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Shutdown flushes and stops the SDK MeterProvider. It is a named operation, so
// callers log a non-nil error rather than dropping it.
func (m *PrometheusMetrics) Shutdown(ctx context.Context) error {
	if err := m.provider.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown meter provider: %w", err)
	}
	return nil
}

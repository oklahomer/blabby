package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scrape drives the bundle's HTTP handler and returns the exposition body.
func scrape(t *testing.T, m *PrometheusMetrics) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// TestNewPrometheusMetricsExposesInstruments builds the bundle, records a
// counter through the OTel MeterProvider, and asserts the instrument is visible
// in the Prometheus scrape output.
func TestNewPrometheusMetricsExposesInstruments(t *testing.T) {
	m, err := NewPrometheusMetrics()
	if err != nil {
		t.Fatalf("NewPrometheusMetrics: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	counter, err := m.MeterProvider().Meter("telemetry_test").Int64Counter("telemetry_test_events")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(context.Background(), 1)

	body := scrape(t, m)
	if !strings.Contains(body, "telemetry_test_events") {
		t.Errorf("scrape body missing recorded counter; got:\n%s", body)
	}
}

// TestNewPrometheusMetricsExposesGoRuntimeMetrics confirms the free Go runtime
// collector is registered on the dedicated registry.
func TestNewPrometheusMetricsExposesGoRuntimeMetrics(t *testing.T) {
	m, err := NewPrometheusMetrics()
	if err != nil {
		t.Fatalf("NewPrometheusMetrics: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	body := scrape(t, m)
	if !strings.Contains(body, "go_goroutines") {
		t.Errorf("scrape body missing go_goroutines runtime metric; got:\n%s", body)
	}
}

// TestShutdownReturnsNil verifies Shutdown reports success on the SDK provider.
func TestShutdownReturnsNil(t *testing.T) {
	m, err := NewPrometheusMetrics()
	if err != nil {
		t.Fatalf("NewPrometheusMetrics: %v", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestTwoInstancesCoexist is the regression test for the process-global
// default-registerer pitfall: each bundle owns a dedicated prometheus.Registry,
// so a second instance neither panics on duplicate collector registration nor
// leaks the first instance's instruments into its own scrape.
func TestTwoInstancesCoexist(t *testing.T) {
	first, err := NewPrometheusMetrics()
	if err != nil {
		t.Fatalf("NewPrometheusMetrics (first): %v", err)
	}
	t.Cleanup(func() { _ = first.Shutdown(context.Background()) })

	second, err := NewPrometheusMetrics()
	if err != nil {
		t.Fatalf("NewPrometheusMetrics (second): %v", err)
	}
	t.Cleanup(func() { _ = second.Shutdown(context.Background()) })

	firstCounter, err := first.MeterProvider().Meter("first").Int64Counter("telemetry_first_only")
	if err != nil {
		t.Fatalf("first Int64Counter: %v", err)
	}
	firstCounter.Add(context.Background(), 1)

	firstBody := scrape(t, first)
	if !strings.Contains(firstBody, "telemetry_first_only") {
		t.Errorf("first scrape missing its own counter; got:\n%s", firstBody)
	}
	secondBody := scrape(t, second)
	if strings.Contains(secondBody, "telemetry_first_only") {
		t.Errorf("second scrape leaked the first instance's counter; registries are not isolated:\n%s", secondBody)
	}
}

package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/telemetry"
)

// metricsGateway builds a Gateway whose internal listener serves a real
// Prometheus scrape handler, so the route test asserts against the genuine
// exposition (content type and runtime metrics), not a stub.
func metricsGateway(t *testing.T) *Gateway {
	t.Helper()
	m, err := telemetry.NewPrometheusMetrics()
	if err != nil {
		t.Fatalf("NewPrometheusMetrics: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return NewGateway(Deps{Metrics: m.HTTPHandler()})
}

func TestMetricsRouteServesScrapeWhenEnabled(t *testing.T) {
	g := metricsGateway(t)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	g.RegisterInternalRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want a Prometheus text exposition type", ct)
	}
	if !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Errorf("scrape body missing runtime metrics; got:\n%s", rec.Body.String())
	}
}

func TestMetricsRouteWrongMethodReturns405(t *testing.T) {
	g := metricsGateway(t)

	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	rec := httptest.NewRecorder()
	g.RegisterInternalRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /metrics status = %d, want 405", rec.Code)
	}
}

func TestMetricsRouteAbsentWhenDisabled(t *testing.T) {
	// No Metrics handler injected: the feature is off, so the route must not
	// exist and the request falls through to the 404 catch-all.
	g := NewGateway(Deps{})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	g.RegisterInternalRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /metrics status = %d, want 404 when --metrics is off", rec.Code)
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseConfig confirms the backend exposes the cluster flags plus the
// optional --metrics-listen (no HTTP/auth flags) and that clusterboot's
// validation surfaces through parseConfig. The exhaustive cluster-flag cases
// live in the clusterboot package.
func TestParseConfig(t *testing.T) {
	tests := []struct {
		name              string
		args              []string
		wantErr           bool
		errMatch          string
		wantMultiNode     bool
		wantMetricsListen string
	}{
		{
			name:          "defaults single-node",
			args:          nil,
			wantMultiNode: false,
		},
		{
			name: "multi-node valid",
			args: []string{
				"--seeds", "127.0.0.1:6330",
				"--advertised-host", "127.0.0.1:8092",
				"--cluster-port", "8092",
			},
			wantMultiNode: true,
		},
		{
			name:              "metrics-listen kept verbatim",
			args:              []string{"--metrics-listen", "127.0.0.1:9464"},
			wantMultiNode:     false,
			wantMetricsListen: "127.0.0.1:9464",
		},
		{
			name:              "metrics-listen empty disables the server",
			args:              nil,
			wantMultiNode:     false,
			wantMetricsListen: "",
		},
		{
			name:     "metrics-listen without port rejected",
			args:     []string{"--metrics-listen", "localhost"},
			wantErr:  true,
			errMatch: "metrics-listen",
		},
		{
			name:     "cluster validation error surfaces",
			args:     []string{"--seeds", "127.0.0.1:6330", "--cluster-port", "8092"},
			wantErr:  true,
			errMatch: "advertised-host",
		},
		{
			name:     "no HTTP flags: --listen rejected",
			args:     []string{"--listen", ":9000"},
			wantErr:  true,
			errMatch: "listen",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCfg, _, got, err := parseConfig(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got config %+v", got)
				}
				if tc.errMatch != "" && !strings.Contains(err.Error(), tc.errMatch) {
					t.Fatalf("error %q does not contain %q", err, tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.MultiNode() != tc.wantMultiNode {
				t.Errorf("MultiNode() = %v, want %v", got.MultiNode(), tc.wantMultiNode)
			}
			if gotCfg.metricsListen != tc.wantMetricsListen {
				t.Errorf("metricsListen = %q, want %q", gotCfg.metricsListen, tc.wantMetricsListen)
			}
		})
	}
}

// TestMetricsMuxRouting exercises the dedicated metrics listener's routing
// without binding a port: GET /metrics reaches the injected handler, other
// methods get 405, and unknown paths get 404.
func TestMetricsMuxRouting(t *testing.T) {
	const body = "telemetry_test_metric 1"
	h := metricsMux(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"GET metrics serves handler", http.MethodGet, "/metrics", http.StatusOK},
		{"POST metrics is 405", http.MethodPost, "/metrics", http.StatusMethodNotAllowed},
		{"unknown path is 404", http.MethodGet, "/nope", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK && !strings.Contains(rec.Body.String(), body) {
				t.Errorf("GET /metrics body = %q, want it to contain %q", rec.Body.String(), body)
			}
		})
	}
}

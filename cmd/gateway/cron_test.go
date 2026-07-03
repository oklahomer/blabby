package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateGCSchedule(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "every descriptor", raw: "@every 1m", want: "@every 1m"},
		{name: "standard five-field spec", raw: "*/5 * * * *", want: "*/5 * * * *"},
		{name: "off disables", raw: "off", want: ""},
		{name: "off is case-insensitive and trimmed", raw: "  OFF ", want: ""},
		{name: "empty rejected", raw: "", wantErr: true},
		{name: "gibberish rejected", raw: "every minute please", wantErr: true},
		{name: "six-field spec rejected", raw: "* * * * * *", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateGCSchedule(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateGCSchedule(%q) = %q, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateGCSchedule(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("validateGCSchedule(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestInternalJobURL(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		want    string
		wantErr bool
	}{
		{name: "loopback", addr: "127.0.0.1:9090", want: "http://127.0.0.1:9090/internal/jobs/pending-account-gc"},
		{name: "empty host becomes loopback", addr: ":9090", want: "http://127.0.0.1:9090/internal/jobs/pending-account-gc"},
		{name: "ipv4 wildcard becomes loopback", addr: "0.0.0.0:9090", want: "http://127.0.0.1:9090/internal/jobs/pending-account-gc"},
		{name: "ipv6 wildcard becomes loopback", addr: "[::]:9090", want: "http://127.0.0.1:9090/internal/jobs/pending-account-gc"},
		{name: "concrete host kept", addr: "10.0.0.5:9090", want: "http://10.0.0.5:9090/internal/jobs/pending-account-gc"},
		{name: "ipv6 host kept and bracketed", addr: "[::1]:9090", want: "http://[::1]:9090/internal/jobs/pending-account-gc"},
		{name: "missing port rejected", addr: "localhost", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := internalJobURL(tc.addr, pendingAccountGCPath)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("internalJobURL(%q) = %q, want error", tc.addr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("internalJobURL(%q): %v", tc.addr, err)
			}
			if got != tc.want {
				t.Errorf("internalJobURL(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

func TestTriggerPendingAccountGC_PostsToEndpoint(t *testing.T) {
	var calls atomic.Int32
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: time.Second}
	triggerPendingAccountGC(client, srv.URL+pendingAccountGCPath)

	if calls.Load() != 1 {
		t.Fatalf("endpoint called %d times, want 1", calls.Load())
	}
	if gotMethod != http.MethodPost || gotPath != pendingAccountGCPath {
		t.Errorf("request = %s %s, want POST %s", gotMethod, gotPath, pendingAccountGCPath)
	}
}

func TestTriggerPendingAccountGC_FailureDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // connection refused from here on

	client := &http.Client{Timeout: time.Second}
	// The assertion is that a refused connection is absorbed (logged), not panicked.
	triggerPendingAccountGC(client, srv.URL+pendingAccountGCPath)
}

func TestStartPendingAccountGCCron_TriggersOnSchedule(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	c, err := startPendingAccountGCCron("@every 100ms", addr)
	if err != nil {
		t.Fatalf("startPendingAccountGCCron: %v", err)
	}
	defer c.Stop()

	deadline := time.After(3 * time.Second)
	for calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("cron never triggered the endpoint")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

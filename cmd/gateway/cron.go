package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	// pendingAccountGCPath is the internal job endpoint the cron POSTs to. The
	// local cron and a future external scheduler share this one trigger contract,
	// so swapping schedulers never touches the backend.
	pendingAccountGCPath = "/internal/jobs/pending-account-gc"

	// gcTriggerTimeout bounds one trigger POST. The endpoint replies without
	// awaiting the sweep, so a healthy call returns in milliseconds; the bound only
	// protects the cron slot from a wedged listener.
	gcTriggerTimeout = 10 * time.Second
)

// validateGCSchedule parses the --gc-schedule flag value: a robfig/cron spec is
// returned as given, the literal "off" (case-insensitive) becomes the empty string
// meaning the local cron is disabled, and anything else is an error.
func validateGCSchedule(raw string) (string, error) {
	spec := strings.TrimSpace(raw)
	if strings.EqualFold(spec, gcScheduleOff) {
		return "", nil
	}
	if _, err := cron.ParseStandard(spec); err != nil {
		return "", fmt.Errorf("--gc-schedule %q is not a cron spec or %q: %w", raw, gcScheduleOff, err)
	}
	return spec, nil
}

// startPendingAccountGCCron schedules trigger POSTs to this process's own internal
// job endpoint and starts the scheduler. The caller stops it on shutdown. The
// returned cron recovers a panicking job and skips a tick while the previous one
// still runs, so a slow or failing trigger can never stack calls.
func startPendingAccountGCCron(schedule, internalListenAddr string) (*cron.Cron, error) {
	url, err := internalJobURL(internalListenAddr, pendingAccountGCPath)
	if err != nil {
		return nil, fmt.Errorf("derive internal job URL: %w", err)
	}

	client := &http.Client{Timeout: gcTriggerTimeout}
	logger := cronLogger{}
	c := cron.New(cron.WithChain(cron.Recover(logger), cron.SkipIfStillRunning(logger)))
	if _, err := c.AddFunc(schedule, func() { triggerPendingAccountGC(client, url) }); err != nil {
		return nil, fmt.Errorf("schedule %q: %w", schedule, err)
	}
	c.Start()
	slog.Info("server.gc_cron.started", "schedule", schedule, "url", url)
	return c, nil
}

// internalJobURL derives the URL of this process's own internal listener for the
// given path. A wildcard or empty bind host (":9090", "0.0.0.0:9090", "[::]:9090")
// is reached via loopback.
func internalJobURL(listenAddr, path string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", fmt.Errorf("listen address %q is not host:port: %w", listenAddr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + path, nil
}

// triggerPendingAccountGC POSTs one trigger and logs the outcome. Failures are
// logged at warning level and never propagate: a missed tick is harmless because
// the next tick sweeps the same stale rows.
func triggerPendingAccountGC(client *http.Client, url string) {
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		slog.Warn("server.gc_cron.trigger_failed", "error", err.Error())
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusAccepted:
		slog.Info("server.gc_cron.triggered", "outcome", "accepted")
	case http.StatusOK:
		slog.Info("server.gc_cron.triggered", "outcome", "already_running")
	default:
		slog.Warn("server.gc_cron.trigger_failed", "status", resp.StatusCode)
	}
}

// cronLogger adapts the robfig/cron logger contract to slog. The scheduler's own
// chatter (wakeups, skips) goes to debug; recovered job panics and scheduler
// errors are warnings.
type cronLogger struct{}

func (cronLogger) Info(msg string, keysAndValues ...any) {
	slog.Debug("server.gc_cron: "+msg, keysAndValues...)
}

func (cronLogger) Error(err error, msg string, keysAndValues ...any) {
	slog.Warn("server.gc_cron: "+msg, append([]any{"error", err}, keysAndValues...)...)
}

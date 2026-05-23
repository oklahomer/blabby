package logging_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/logging"
)

// captureStderr replaces os.Stderr with a pipe for the duration of fn, then
// returns whatever fn wrote. Restores the original Stderr in t.Cleanup so
// later tests are unaffected. SetupDefault writes to os.Stderr, so capturing
// the pipe lets the test assert on what SetupDefault actually emitted.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = orig })

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	_ = w.Close()
	return <-done
}

// TestParseLevel covers every accepted level alias plus the unknown
// fallback, exercising the case-insensitivity contract from the godoc.
func TestParseLevel(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantLevel      slog.Level
		wantRecognized bool
	}{
		{"lowercase debug", "debug", slog.LevelDebug, true},
		{"uppercase DEBUG", "DEBUG", slog.LevelDebug, true},
		{"mixed-case Debug", "Debug", slog.LevelDebug, true},
		{"info", "info", slog.LevelInfo, true},
		{"warn", "warn", slog.LevelWarn, true},
		{"warning alias", "warning", slog.LevelWarn, true},
		{"error", "error", slog.LevelError, true},
		{"trim whitespace", "  info  ", slog.LevelInfo, true},
		{"empty falls back", "", slog.LevelInfo, false},
		{"unknown falls back", "verbose", slog.LevelInfo, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotLevel, gotRecognized := logging.ParseLevel(tc.input)
			if gotLevel != tc.wantLevel {
				t.Errorf("ParseLevel(%q) level = %v, want %v", tc.input, gotLevel, tc.wantLevel)
			}
			if gotRecognized != tc.wantRecognized {
				t.Errorf("ParseLevel(%q) recognized = %v, want %v", tc.input, gotRecognized, tc.wantRecognized)
			}
		})
	}
}

// withCapturedDefault redirects slog.Default()'s output to buf for the
// duration of the test. It does NOT call SetupDefault — tests that need
// SetupDefault's installation behavior should call it explicitly within
// the captured window. The pre-test default is restored on cleanup.
func withCapturedDefault(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &bytes.Buffer{}
}

func TestSetupDefault_DefaultsToInfo(t *testing.T) {
	_ = withCapturedDefault(t)
	t.Setenv("BLABBY_LOG_LEVEL", "")

	level := logging.SetupDefault()
	if level != slog.LevelInfo {
		t.Fatalf("SetupDefault() with unset env = %v, want %v", level, slog.LevelInfo)
	}
}

func TestSetupDefault_HonoursDebug(t *testing.T) {
	_ = withCapturedDefault(t)
	t.Setenv("BLABBY_LOG_LEVEL", "debug")

	level := logging.SetupDefault()
	if level != slog.LevelDebug {
		t.Errorf("SetupDefault() with debug env = %v, want %v", level, slog.LevelDebug)
	}
	if !slog.Default().Enabled(t.Context(), slog.LevelDebug) {
		t.Errorf("slog.Default() did not enable Debug after SetupDefault(debug)")
	}
}

func TestSetupDefault_HonoursWarn(t *testing.T) {
	_ = withCapturedDefault(t)
	t.Setenv("BLABBY_LOG_LEVEL", "WARN")

	level := logging.SetupDefault()
	if level != slog.LevelWarn {
		t.Errorf("SetupDefault() with WARN env = %v, want %v", level, slog.LevelWarn)
	}
	if slog.Default().Enabled(t.Context(), slog.LevelInfo) {
		t.Errorf("slog.Default() Info should be disabled at warn level")
	}
}

// TestSetupDefault_UnknownFallbackEmitsWarning verifies the unknown-input
// branch: ParseLevel returns (info, false), SetupDefault installs the
// info-level handler, and a single Warn line surfaces the offending value
// so an operator can spot the misconfiguration without spelunking through
// startup output. The captured stderr is the actual emission, not a probe.
func TestSetupDefault_UnknownFallbackEmitsWarning(t *testing.T) {
	_ = withCapturedDefault(t)
	t.Setenv("BLABBY_LOG_LEVEL", "totally-bogus")

	var level slog.Level
	out := captureStderr(t, func() {
		level = logging.SetupDefault()
	})
	if level != slog.LevelInfo {
		t.Errorf("SetupDefault() with bogus env = %v, want %v", level, slog.LevelInfo)
	}

	out = strings.TrimSpace(out)
	if out == "" {
		t.Fatalf("expected a warning line on stderr, got nothing")
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(out), &entry); err != nil {
		t.Fatalf("stderr line was not valid JSON: %v\nout=%s", err, out)
	}
	if entry["msg"] != "logging.unknown_level" {
		t.Errorf("msg = %v, want logging.unknown_level", entry["msg"])
	}
	if entry["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", entry["level"])
	}
	if entry["value"] != "totally-bogus" {
		t.Errorf("value = %v, want totally-bogus", entry["value"])
	}
	if entry["resolved_level"] != slog.LevelInfo.String() {
		t.Errorf("resolved_level = %v, want %v", entry["resolved_level"], slog.LevelInfo.String())
	}
}

// TestSetupDefault_InstallsJSONHandler verifies the installed handler
// produces JSON output (newline-delimited objects), so downstream
// log-collection assumes a stable shape. Captures stderr to assert on the
// actual handler SetupDefault installs, not on a probe handler.
func TestSetupDefault_InstallsJSONHandler(t *testing.T) {
	_ = withCapturedDefault(t)
	t.Setenv("BLABBY_LOG_LEVEL", "info")

	out := captureStderr(t, func() {
		logging.SetupDefault()
		slog.Info("test.line", "key", "value")
	})

	out = strings.TrimSpace(out)
	if !strings.HasPrefix(out, "{") || !strings.HasSuffix(out, "}") {
		t.Errorf("expected JSON object, got %q", out)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(out), &entry); err != nil {
		t.Fatalf("output is not JSON: %v\nout=%s", err, out)
	}
	if entry["msg"] != "test.line" {
		t.Errorf("msg = %v, want test.line", entry["msg"])
	}
	if entry["key"] != "value" {
		t.Errorf("key = %v, want value", entry["key"])
	}
}

package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/logging"
)

// newJSONBuffer returns a JSON handler writing to buf at LevelDebug, so the
// only level gate these tests exercise is the min-level wrapper itself, not the
// inner handler.
func newJSONBuffer() (*bytes.Buffer, slog.Handler) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return buf, h
}

// parseJSONLines splits newline-delimited JSON log output into one map per line.
func parseJSONLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(raw), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line is not valid JSON: %v\nline=%s", err, l)
		}
		lines = append(lines, m)
	}
	return lines
}

// TestMinLevelHandler_FiltersByLevel checks the core contract: records below the
// configured minimum are dropped; records at or above it pass through.
func TestMinLevelHandler_FiltersByLevel(t *testing.T) {
	tests := []struct {
		name     string
		min      slog.Level
		record   slog.Level
		wantEmit bool
	}{
		{"warn min drops info", slog.LevelWarn, slog.LevelInfo, false},
		{"warn min drops debug", slog.LevelWarn, slog.LevelDebug, false},
		{"warn min passes warn", slog.LevelWarn, slog.LevelWarn, true},
		{"warn min passes error", slog.LevelWarn, slog.LevelError, true},
		{"info min passes info", slog.LevelInfo, slog.LevelInfo, true},
		{"error min drops warn", slog.LevelError, slog.LevelWarn, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf, inner := newJSONBuffer()
			logger := slog.New(logging.NewMinLevelHandler(inner, tc.min))

			logger.Log(context.Background(), tc.record, "probe.line")

			lines := parseJSONLines(t, buf.String())
			if tc.wantEmit && len(lines) != 1 {
				t.Fatalf("want 1 emitted line, got %d: %q", len(lines), buf.String())
			}
			if !tc.wantEmit && len(lines) != 0 {
				t.Fatalf("want 0 emitted lines, got %d: %q", len(lines), buf.String())
			}
		})
	}
}

// TestMinLevelHandler_WithAttrsPreserved proves WithAttrs wraps-and-delegates:
// attributes added via With(...) survive the wrapper and appear on the record.
// A naive wrapper that returns itself unchanged would drop them.
func TestMinLevelHandler_WithAttrsPreserved(t *testing.T) {
	buf, inner := newJSONBuffer()
	logger := slog.New(logging.NewMinLevelHandler(inner, slog.LevelWarn)).
		With("lib", "proto.actor").
		With("system", "sys-1")

	logger.Warn("probe.line")

	lines := parseJSONLines(t, buf.String())
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d: %q", len(lines), buf.String())
	}
	if lines[0]["lib"] != "proto.actor" {
		t.Errorf("lib = %v, want proto.actor (WithAttrs must wrap-and-delegate)", lines[0]["lib"])
	}
	if lines[0]["system"] != "sys-1" {
		t.Errorf("system = %v, want sys-1 (WithAttrs must wrap-and-delegate)", lines[0]["system"])
	}
}

// TestMinLevelHandler_WithGroupPreserved proves WithGroup wraps-and-delegates:
// attributes logged after WithGroup are nested under the group name.
func TestMinLevelHandler_WithGroupPreserved(t *testing.T) {
	buf, inner := newJSONBuffer()
	logger := slog.New(logging.NewMinLevelHandler(inner, slog.LevelWarn)).WithGroup("g")

	logger.Warn("probe.line", "k", "v")

	lines := parseJSONLines(t, buf.String())
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d: %q", len(lines), buf.String())
	}
	grp, ok := lines[0]["g"].(map[string]any)
	if !ok {
		t.Fatalf("group g missing or wrong type: %v (WithGroup must wrap-and-delegate)", lines[0]["g"])
	}
	if grp["k"] != "v" {
		t.Errorf("g.k = %v, want v", grp["k"])
	}
}

// TestMinLevelHandler_Enabled confirms Enabled agrees with Handle's gate so the
// standard slog fast-path (Enabled check before record construction) is honored.
func TestMinLevelHandler_Enabled(t *testing.T) {
	_, inner := newJSONBuffer()
	h := logging.NewMinLevelHandler(inner, slog.LevelWarn)
	ctx := context.Background()

	if h.Enabled(ctx, slog.LevelInfo) {
		t.Error("Enabled(Info) = true, want false at Warn min")
	}
	if !h.Enabled(ctx, slog.LevelWarn) {
		t.Error("Enabled(Warn) = false, want true at Warn min")
	}
	if !h.Enabled(ctx, slog.LevelError) {
		t.Error("Enabled(Error) = false, want true at Warn min")
	}
}

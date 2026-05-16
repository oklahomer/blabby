// Package logging configures the process-level *slog.Logger that every other
// package writes to via slog.Default().
//
// The split between this package (process setup) and internal/middleware
// (per-message envelope emission) is deliberate: setup is a binary concern,
// middleware is an actor-system concern, and the two share no types.
//
// JSON on os.Stderr is the only output mode. Sampling, rotation, and
// aggregation are downstream-operator concerns.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// envLevelKey is the environment variable consulted by SetupDefault. It is
// exported as a constant so binaries and tests can refer to it without
// hard-coding the string in two places.
const envLevelKey = "BLABBY_LOG_LEVEL"

// SetupDefault installs a *slog.JSONHandler writing to os.Stderr as the
// process-wide slog.Default(). The handler's level is taken from the
// BLABBY_LOG_LEVEL environment variable; ParseLevel describes the accepted
// values.
//
// SetupDefault returns the resolved slog.Level so the binary can log it
// after the handler is in place. Repeated calls overwrite the previous
// default; tests that need to restore state should snapshot slog.Default()
// before calling and restore in t.Cleanup.
//
// Unknown values fall back to slog.LevelInfo and emit a single warning at
// the resolved level identifying the offending input.
func SetupDefault() slog.Level {
	raw := os.Getenv(envLevelKey)
	level, recognized := ParseLevel(raw)

	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))

	if raw != "" && !recognized {
		slog.Warn("logging.unknown_level",
			"env_key", envLevelKey,
			"value", raw,
			"resolved_level", level.String(),
		)
	}
	return level
}

// ParseLevel converts a case-insensitive level string (debug/info/warn/error)
// to its slog.Level. Empty or unrecognized input returns (slog.LevelInfo,
// false); the caller decides whether to surface a warning.
func ParseLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}

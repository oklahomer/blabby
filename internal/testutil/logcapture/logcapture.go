// Package logcapture routes the process-default slog output into a
// goroutine-safe buffer for the duration of a test.
//
// Grain and middleware tests assert on log lines that actor mailbox
// goroutines emit while the test goroutine reads the buffer — a plain
// bytes.Buffer is not safe for that combination, and every package used to
// hand-roll its own snapshot/restore of slog.Default around a private buffer
// type. JSON and Text install a capture handler as the default logger and
// restore the previous default in a test cleanup.
package logcapture

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
)

// Buffer is a goroutine-safe sink for captured log output. The zero value is
// ready to use.
type Buffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p to the buffer. Safe for concurrent use with String.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns the log stream captured so far.
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// JSON captures the default slog output as JSON lines at the given level,
// restoring the previous default logger when the test ends.
func JSON(tb testing.TB, level slog.Level) *Buffer {
	tb.Helper()
	buf := &Buffer{}
	install(tb, slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})))
	return buf
}

// Text captures the default slog output as text lines at the handler's
// default level (info), restoring the previous default logger when the test
// ends.
func Text(tb testing.TB) *Buffer {
	tb.Helper()
	buf := &Buffer{}
	install(tb, slog.New(slog.NewTextHandler(buf, nil)))
	return buf
}

func install(tb testing.TB, logger *slog.Logger) {
	tb.Helper()
	prev := slog.Default()
	slog.SetDefault(logger)
	tb.Cleanup(func() { slog.SetDefault(prev) })
}

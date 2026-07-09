package logcapture

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestJSONCapturesAtLevelAndRestoresDefault(t *testing.T) {
	prev := slog.Default()

	t.Run("capture", func(t *testing.T) {
		buf := JSON(t, slog.LevelDebug)
		slog.Debug("captured-debug", "k", "v")
		out := buf.String()
		if !strings.Contains(out, `"msg":"captured-debug"`) || !strings.Contains(out, `"k":"v"`) {
			t.Errorf("JSON capture missing debug line: %s", out)
		}
	})

	if slog.Default() != prev {
		t.Error("JSON did not restore the previous default logger on cleanup")
	}
}

func TestTextCapturesAtInfoAndRestoresDefault(t *testing.T) {
	prev := slog.Default()

	t.Run("capture", func(t *testing.T) {
		buf := Text(t)
		slog.Debug("filtered-debug")
		slog.Info("captured-info")
		out := buf.String()
		if strings.Contains(out, "filtered-debug") {
			t.Errorf("Text captured a debug line below its info level: %s", out)
		}
		if !strings.Contains(out, "msg=captured-info") {
			t.Errorf("Text capture missing info line: %s", out)
		}
	})

	if slog.Default() != prev {
		t.Error("Text did not restore the previous default logger on cleanup")
	}
}

// TestBufferConcurrentWriteAndRead exercises Write and String from separate
// goroutines; the race detector is the assertion.
func TestBufferConcurrentWriteAndRead(t *testing.T) {
	buf := JSON(t, slog.LevelInfo)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				slog.Info("concurrent-line")
			}
		}()
	}
	for i := 0; i < 100; i++ {
		_ = buf.String()
	}
	wg.Wait()

	if got := strings.Count(buf.String(), "concurrent-line"); got != 200 {
		t.Errorf("captured %d concurrent lines, want 200", got)
	}
}

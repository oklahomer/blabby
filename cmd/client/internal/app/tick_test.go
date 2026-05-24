package app

import (
	"testing"
	"time"
)

func TestTickEverySecondFires(t *testing.T) {
	cmd := tickEverySecond()
	if cmd == nil {
		t.Fatal("tickEverySecond returned nil cmd")
	}

	done := make(chan tickMsg, 1)
	go func() {
		msg := cmd()
		tm, ok := msg.(tickMsg)
		if !ok {
			t.Errorf("expected tickMsg, got %T", msg)
			return
		}
		done <- tm
	}()

	select {
	case <-done:
		// Fired.
	case <-time.After(3 * time.Second):
		t.Fatal("tick did not fire within 3 seconds")
	}
}

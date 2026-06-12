package room

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/asynkron/protoactor-go/actor"
)

func TestClassifyFanoutFailure(t *testing.T) {
	for _, reason := range []any{errors.New("unexpected"), nil, "boom", 42} {
		if got := classifyFanoutFailure(reason); got != actor.RestartDirective {
			t.Errorf("classifyFanoutFailure(%v) = %v, want %v", reason, got, actor.RestartDirective)
		}
	}
}

func TestFanoutAttrsFromJob(t *testing.T) {
	t.Run("notify job carries grain identity", func(t *testing.T) {
		attrs := fanoutAttrs(actor.NewPID("a", "1"), &fanoutNotify{grainKind: "RoomGrain", grainID: "general"})
		assertAttr(t, attrs, "grain_type", "RoomGrain")
		assertAttr(t, attrs, "grain_id", "general")
	})
	t.Run("forward job carries grain identity", func(t *testing.T) {
		attrs := fanoutAttrs(actor.NewPID("a", "1"), &fanoutForward{grainKind: "RoomGrain", grainID: "random"})
		assertAttr(t, attrs, "grain_type", "RoomGrain")
		assertAttr(t, attrs, "grain_id", "random")
	})
	t.Run("unknown message falls back to a usable envelope", func(t *testing.T) {
		attrs := fanoutAttrs(actor.NewPID("addr", "fanout/9"), &struct{}{})
		assertAttr(t, attrs, "grain_type", "RoomGrain")
		assertAttrPresent(t, attrs, "actor_path")
	})
}

func assertAttr(t *testing.T, attrs []slog.Attr, key, want string) {
	t.Helper()
	for _, attr := range attrs {
		if attr.Key == key {
			if got := attr.Value.String(); got != want {
				t.Errorf("attr %q = %q, want %q", key, got, want)
			}
			return
		}
	}
	t.Errorf("attr %q not found in %v", key, attrs)
}

func assertAttrPresent(t *testing.T, attrs []slog.Attr, key string) {
	t.Helper()
	for _, attr := range attrs {
		if attr.Key == key {
			if attr.Value.String() == "" {
				t.Errorf("attr %q is empty", key)
			}
			return
		}
	}
	t.Errorf("attr %q not found in %v", key, attrs)
}

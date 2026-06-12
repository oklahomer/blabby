package connection

import (
	"errors"
	"testing"

	"github.com/asynkron/protoactor-go/actor"
)

func TestClassifyConnectionFailure(t *testing.T) {
	tests := []struct {
		name   string
		reason any
		want   actor.Directive
	}{
		{
			name:   "backpressure stops the connection",
			reason: &outboundBackpressureError{},
			want:   actor.StopDirective,
		},
		{
			name:   "unexpected error stops under root guardian",
			reason: errors.New("something unmodeled"),
			want:   actor.StopDirective,
		},
		{
			name:   "nil reason stops under root guardian",
			reason: nil,
			want:   actor.StopDirective,
		},
		{
			name:   "raw panic value stops under root guardian",
			reason: "boom",
			want:   actor.StopDirective,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyConnectionFailure(tc.reason); got != tc.want {
				t.Errorf("classifyConnectionFailure(%v) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}

// The connection decider must never restart or escalate: a UserConnection's
// identity is its WebSocket, and the root guardian cannot apply escalation.
func TestClassifyConnectionFailureNeverRestartsOrEscalates(t *testing.T) {
	reasons := []any{&outboundBackpressureError{}, errors.New("x"), nil, "panic", 1}
	for _, r := range reasons {
		switch got := classifyConnectionFailure(r); got {
		case actor.RestartDirective, actor.EscalateDirective:
			t.Errorf("classifyConnectionFailure(%v) = %v; connection must only stop", r, got)
		}
	}
}

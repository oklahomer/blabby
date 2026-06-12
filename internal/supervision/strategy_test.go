package supervision

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
)

// spySupervisor records which Supervisor action the strategy applied so a test
// can assert the directive without standing up a real actor hierarchy.
type spySupervisor struct {
	resumed   []*actor.PID
	restarted []*actor.PID
	stopped   []*actor.PID
	escalated bool
}

func (s *spySupervisor) Children() []*actor.PID                       { return nil }
func (s *spySupervisor) EscalateFailure(_ interface{}, _ interface{}) { s.escalated = true }
func (s *spySupervisor) RestartChildren(pids ...*actor.PID) {
	s.restarted = append(s.restarted, pids...)
}
func (s *spySupervisor) StopChildren(pids ...*actor.PID)   { s.stopped = append(s.stopped, pids...) }
func (s *spySupervisor) ResumeChildren(pids ...*actor.PID) { s.resumed = append(s.resumed, pids...) }

func staticAttrs(_ *actor.PID, _ any) []slog.Attr {
	return []slog.Attr{
		slog.String("grain_type", "RoomGrain"),
		slog.String("grain_id", "general"),
	}
}

// alwaysDecide returns a decider that always yields d, so a test can drive any
// directive branch.
func alwaysDecide(d actor.Directive) actor.DeciderFunc {
	return func(_ interface{}) actor.Directive { return d }
}

type capturedLog struct {
	logger *slog.Logger
	buf    *bytes.Buffer
}

func captureLogger() capturedLog {
	buf := &bytes.Buffer{}
	return capturedLog{logger: slog.New(slog.NewJSONHandler(buf, nil)), buf: buf}
}

// only returns the single JSON log record emitted, failing if zero or many.
func (c capturedLog) only(t *testing.T) map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(c.buf.Bytes()), []byte("\n"))
	if len(lines) != 1 || len(lines[0]) == 0 {
		t.Fatalf("expected exactly one log line, got %d: %s", len(lines), c.buf.String())
	}
	var m map[string]any
	if err := json.Unmarshal(lines[0], &m); err != nil {
		t.Fatalf("log line is not valid JSON: %v (%s)", err, lines[0])
	}
	return m
}

func handle(t *testing.T, directive actor.Directive, reason, message any) (*spySupervisor, capturedLog) {
	t.Helper()
	logs := captureLogger()
	decider := alwaysDecide(directive)
	strategy, err := NewLoggingStrategy(Config{
		Event:   "test.supervision",
		Decider: decider,
		Attrs:   staticAttrs,
		Logger:  logs.logger,
		Apply:   actor.NewOneForOneStrategy(10, time.Second, decider),
	})
	if err != nil {
		t.Fatalf("NewLoggingStrategy: %v", err)
	}
	spy := &spySupervisor{}
	system := actor.NewActorSystem()
	t.Cleanup(system.Shutdown)
	strategy.HandleFailure(system, spy, actor.NewPID("addr", "fanout/1"), actor.NewRestartStatistics(), reason, message)
	return spy, logs
}

func TestLoggingStrategyAppliesDirective(t *testing.T) {
	tests := []struct {
		name      string
		directive actor.Directive
		wantLevel string
		assert    func(t *testing.T, spy *spySupervisor)
	}{
		{
			name:      "resume skips the message",
			directive: actor.ResumeDirective,
			wantLevel: "WARN",
			assert: func(t *testing.T, spy *spySupervisor) {
				if len(spy.resumed) != 1 {
					t.Fatalf("expected one resumed child, got %v", spy.resumed)
				}
			},
		},
		{
			name:      "restart rebuilds the child",
			directive: actor.RestartDirective,
			wantLevel: "WARN",
			assert: func(t *testing.T, spy *spySupervisor) {
				if len(spy.restarted) != 1 {
					t.Fatalf("expected one restarted child, got %v", spy.restarted)
				}
			},
		},
		{
			name:      "stop tears down",
			directive: actor.StopDirective,
			wantLevel: "ERROR",
			assert: func(t *testing.T, spy *spySupervisor) {
				if len(spy.stopped) != 1 {
					t.Fatalf("expected one stopped child, got %v", spy.stopped)
				}
			},
		},
		{
			name:      "escalate hands to parent",
			directive: actor.EscalateDirective,
			wantLevel: "ERROR",
			assert: func(t *testing.T, spy *spySupervisor) {
				if !spy.escalated {
					t.Fatal("expected escalation")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spy, logs := handle(t, tc.directive, errors.New("boom"), &struct{}{})

			tc.assert(t, spy)

			line := logs.only(t)
			if line["level"] != tc.wantLevel {
				t.Errorf("level = %v, want %v", line["level"], tc.wantLevel)
			}
			if line["msg"] != "test.supervision" {
				t.Errorf("event = %v, want test.supervision", line["msg"])
			}
			if line["directive"] != tc.directive.String() {
				t.Errorf("directive = %v, want %v", line["directive"], tc.directive.String())
			}
		})
	}
}

func TestLoggingStrategyEnvelope(t *testing.T) {
	_, logs := handle(t, actor.StopDirective, errors.New("notifier exploded"), &struct{ payload string }{payload: "secret-token"})

	line := logs.only(t)
	for k, want := range map[string]string{
		"grain_type": "RoomGrain",
		"grain_id":   "general",
		"error":      "notifier exploded",
	} {
		if line[k] != want {
			t.Errorf("%s = %v, want %v", k, line[k], want)
		}
	}
	// msg_type is the Go type name only — never the struct's field values.
	if mt, _ := line["msg_type"].(string); mt == "" {
		t.Error("msg_type missing")
	}
	if got := logs.buf.String(); bytes.Contains([]byte(got), []byte("secret-token")) {
		t.Errorf("log leaked message payload: %s", got)
	}
}

func TestNewLoggingStrategyRejectsInvalidConfiguration(t *testing.T) {
	valid := Config{
		Event:   "test.supervision",
		Decider: alwaysDecide(actor.StopDirective),
		Attrs:   staticAttrs,
		Apply:   actor.NewRestartingStrategy(),
	}
	tests := []struct {
		name       string
		invalidate func(cfg Config) Config
	}{
		{name: "empty event", invalidate: func(cfg Config) Config { cfg.Event = " "; return cfg }},
		{name: "nil decider", invalidate: func(cfg Config) Config { cfg.Decider = nil; return cfg }},
		{name: "nil attrs", invalidate: func(cfg Config) Config { cfg.Attrs = nil; return cfg }},
		{name: "nil apply", invalidate: func(cfg Config) Config { cfg.Apply = nil; return cfg }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewLoggingStrategy(tc.invalidate(valid)); err == nil {
				t.Fatal("NewLoggingStrategy() error = nil, want invalid configuration error")
			}
		})
	}
}

// stringerReason carries a payload but exposes only what String() chooses to.
type stringerReason struct{ secret string }

func (s stringerReason) String() string { return "stringer text" }

// opaqueReason is a payload-bearing panic value with no Error() or String();
// errText must reduce it to its type name rather than dump its fields.
type opaqueReason struct{ token string }

func TestErrText(t *testing.T) {
	tests := []struct {
		name   string
		reason any
		want   string
	}{
		{"nil", nil, ""},
		{"error", errors.New("kaboom"), "kaboom"},
		{"string", "raw panic", "raw panic"},
		{"stringer", stringerReason{secret: "hidden"}, "stringer text"},
		{"opaque struct", opaqueReason{token: "secret-token"}, "supervision.opaqueReason"},
		{"other", 42, "int"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errText(tc.reason); got != tc.want {
				t.Errorf("errText(%v) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

func TestTypeNameTrimsPointer(t *testing.T) {
	if got := typeName(&struct{}{}); got == "" || got[0] == '*' {
		t.Errorf("typeName should trim leading '*', got %q", got)
	}
}

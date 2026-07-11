package clusterboot

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"

	"github.com/oklahomer/blabby/internal/testutil/logcapture"
)

// allowedDeadLetterKeys is the complete set of fields a dead-letter observed
// line may carry. Asserting against it proves the line never carries the
// undeliverable message body — only its type name — nor any other payload.
var allowedDeadLetterKeys = map[string]bool{
	"time":     true,
	"level":    true,
	"msg":      true,
	"pid":      true,
	"msg_type": true,
	"sender":   true,
}

// probeMsg is a named message type so the msg_type field is predictable
// ("clusterboot.probeMsg").
type probeMsg struct{}

// ignoredDeadLetter implements actor.IgnoreDeadLetterLogging, the marker
// interface whose implementers the dead-letter log must skip.
type ignoredDeadLetter struct{}

func (ignoredDeadLetter) IgnoreDeadLetterLogging() {}

// TestDeadLetterLogHandler_ObservedFields drives the handler with a fully
// populated event and asserts the observed line carries exactly the safe
// fields — including sender when present — and nothing else.
func TestDeadLetterLogHandler_ObservedFields(t *testing.T) {
	handle := newDeadLetterLogHandler(100, time.Minute)
	pid := actor.NewPID("nonhost:0", "target")
	sender := actor.NewPID("nonhost:0", "sender")

	lines := deadLetterLines(captureJSONLogs(t, func() {
		handle(&actor.DeadLetterEvent{PID: pid, Message: &probeMsg{}, Sender: sender})
	}))

	if len(lines) != 1 {
		t.Fatalf("got %d server.deadletter.* log lines, want 1: %v", len(lines), lines)
	}
	ln := lines[0]
	if ln["msg"] != "server.deadletter.observed" {
		t.Errorf("msg = %v, want server.deadletter.observed", ln["msg"])
	}
	if ln["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", ln["level"])
	}
	if ln["pid"] != pid.String() {
		t.Errorf("pid = %v, want %v", ln["pid"], pid.String())
	}
	if ln["msg_type"] != "clusterboot.probeMsg" {
		t.Errorf("msg_type = %v, want clusterboot.probeMsg", ln["msg_type"])
	}
	if ln["sender"] != sender.String() {
		t.Errorf("sender = %v, want %v", ln["sender"], sender.String())
	}
	for k := range ln {
		if !allowedDeadLetterKeys[k] {
			t.Errorf("unexpected log field %q (possible payload leak) in %v", k, ln)
		}
	}
}

// TestDeadLetterLogHandler_SenderOmittedWhenNil confirms the sender field is
// absent (not empty) when the dead letter has no sender.
func TestDeadLetterLogHandler_SenderOmittedWhenNil(t *testing.T) {
	handle := newDeadLetterLogHandler(100, time.Minute)
	pid := actor.NewPID("nonhost:0", "target")

	lines := deadLetterLines(captureJSONLogs(t, func() {
		handle(&actor.DeadLetterEvent{PID: pid, Message: &probeMsg{}})
	}))

	if len(lines) != 1 {
		t.Fatalf("got %d server.deadletter.* log lines, want 1: %v", len(lines), lines)
	}
	if _, ok := lines[0]["sender"]; ok {
		t.Errorf("sender field present for a nil sender: %v", lines[0])
	}
}

// TestDeadLetterLogHandler_Ignored proves the handler ignores events that are
// not *actor.DeadLetterEvent and skips dead letters whose message implements
// actor.IgnoreDeadLetterLogging.
func TestDeadLetterLogHandler_Ignored(t *testing.T) {
	handle := newDeadLetterLogHandler(100, time.Minute)

	lines := deadLetterLines(captureJSONLogs(t, func() {
		handle("not a dead letter")
		handle(42)
		handle(&actor.DeadLetterEvent{PID: actor.NewPID("nonhost:0", "t"), Message: ignoredDeadLetter{}})
	}))

	if len(lines) != 0 {
		t.Fatalf("got %d server.deadletter.* log lines, want 0 (non-event ignored, IgnoreDeadLetterLogging skipped): %v", len(lines), lines)
	}
}

// TestDeadLetterLogHandler_Throttles fires more events than the throttle budget
// and asserts the valve closes: only the first event is Open (proto.actor's
// throttle returns Closing at the count-th call), and after the period the
// callback emits one throttled line reporting the dropped remainder. The
// callback runs on a timer goroutine, so the throttled line is polled for with
// a deadline against the goroutine-safe capture buffer.
func TestDeadLetterLogHandler_Throttles(t *testing.T) {
	const count int32 = 2
	handle := newDeadLetterLogHandler(count, 20*time.Millisecond)
	pid := actor.NewPID("nonhost:0", "target")

	buf := logcapture.JSON(t, slog.LevelWarn)

	for i := 0; i < 5; i++ {
		handle(&actor.DeadLetterEvent{PID: pid, Message: &probeMsg{}})
	}

	// Immediately, before the throttle period elapses: exactly one observed
	// line. With count=2 the valve is Open only on the first call and Closing
	// on the second, so a single event passes the != Open gate.
	observed := countByMsg(parseDeadLetterLines(t, buf.String()), "server.deadletter.observed")
	if observed != 1 {
		t.Fatalf("immediate observed lines = %d, want 1; buffer=%q", observed, buf.String())
	}

	// The throttle callback logs from a timer goroutine when the period ends.
	throttled := waitForLine(t, buf, "server.deadletter.throttled", 2*time.Second)
	if throttled == nil {
		t.Fatalf("no server.deadletter.throttled line within deadline; buffer=%q", buf.String())
	}
	// 5 events fired, budget 2, so 3 were dropped.
	if got := throttled["dropped"]; got != float64(3) {
		t.Errorf("dropped = %v, want 3", got)
	}
	for k := range throttled {
		switch k {
		case "time", "level", "msg", "dropped":
		default:
			t.Errorf("unexpected field %q on throttled line: %v", k, throttled)
		}
	}
}

// TestSubscribeDeadLetterLogging_Integration wires the subscription onto a built
// (but unstarted) single-node cluster, sends to a nonexistent local PID, and
// asserts blabby's observed line appears while proto.actor's built-in
// content-leaking [DeadLetter] Info line does not — proving the Warn gate on
// the actor system's logger drops it.
func TestSubscribeDeadLetterLogging_Integration(t *testing.T) {
	lines := captureJSONLogs(t, func() {
		c := Build(Config{bindHost: defaultClusterHost, discoveryPort: defaultDiscoveryPort}, Kinds(testGrainDeps())...)
		sub := SubscribeDeadLetterLogging(c)
		defer c.ActorSystem.EventStream.Unsubscribe(sub)

		system := c.ActorSystem
		// EventStream publication is synchronous on the sending goroutine, so
		// the observed line is present by the time Send returns.
		system.Root.Send(actor.NewPID(system.Address(), "nope"), &struct{ X int }{X: 1})
	})

	var observed []map[string]any
	for _, ln := range lines {
		switch ln["msg"] {
		case "server.deadletter.observed":
			observed = append(observed, ln)
		case "[DeadLetter]":
			t.Errorf("proto.actor built-in [DeadLetter] line leaked into the stream: %v", ln)
		}
	}
	if len(observed) != 1 {
		t.Fatalf("got %d server.deadletter.observed lines, want 1: %v", len(observed), lines)
	}
	if observed[0]["msg_type"] != "struct { X int }" {
		t.Errorf("msg_type = %v, want struct { X int }", observed[0]["msg_type"])
	}
}

// TestSubscribeDeadLetterLogging_ReturnsSubscription confirms the subscription
// is established on the built cluster's EventStream.
func TestSubscribeDeadLetterLogging_ReturnsSubscription(t *testing.T) {
	c := Build(Config{bindHost: defaultClusterHost, discoveryPort: defaultDiscoveryPort}, Kinds(testGrainDeps())...)

	sub := SubscribeDeadLetterLogging(c)
	if sub == nil {
		t.Fatal("SubscribeDeadLetterLogging returned nil")
	}
	c.ActorSystem.EventStream.Unsubscribe(sub)
}

// deadLetterLines keeps only the lines this package's dead-letter handler
// emits. Capturing slog.Default() is capturing a process-wide stream: cluster
// tests that ran earlier leave provider goroutines (automanaged health polls)
// alive briefly after shutdown, and their stray lines can land in a later
// test's buffer. Assertions therefore never count unfiltered lines.
func deadLetterLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, ln := range lines {
		if msg, _ := ln["msg"].(string); strings.HasPrefix(msg, "server.deadletter.") {
			out = append(out, ln)
		}
	}
	return out
}

// parseDeadLetterLines parses the captured buffer into one map per JSON line.
func parseDeadLetterLines(t *testing.T, raw string) []map[string]any {
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

// countByMsg counts captured lines whose msg equals want.
func countByMsg(lines []map[string]any, want string) int {
	n := 0
	for _, ln := range lines {
		if ln["msg"] == want {
			n++
		}
	}
	return n
}

// waitForLine polls the goroutine-safe capture buffer until a line with the
// given msg appears or the timeout elapses, returning the line or nil.
func waitForLine(t *testing.T, buf *logcapture.Buffer, msg string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, ln := range parseDeadLetterLines(t, buf.String()) {
			if ln["msg"] == msg {
				return ln
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

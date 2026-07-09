package middleware_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"

	"github.com/oklahomer/blabby/internal/middleware"
	"github.com/oklahomer/blabby/internal/testutil/logcapture"
)

// fakeReceiverContext is the minimal actor.ReceiverContext implementation
// the middleware needs at unit-test time. The middleware reads Self() to
// build the envelope and forwards the rest to next; nothing else is
// touched, so the rest of the interface is satisfied with no-ops.
//
// Embedding a nil actor.Context is the conventional shortcut for satisfying
// large protoactor interfaces in tests; calls to unset methods panic, which
// is exactly the safety net we want — the middleware is not allowed to
// drift into other ReceiverContext methods.
type fakeReceiverContext struct {
	actor.Context // nil — panics if the middleware uses anything beyond Self()
	self          *actor.PID
}

func (f *fakeReceiverContext) Self() *actor.PID { return f.self }

// roomTestMsg exercises the package-qualified type-name resolution path.
type roomTestMsg struct{ Tag string }

// secretBearingMsg pretends to be a proto whose String()/fmt rendering
// contains a credential — the test asserts the middleware never invokes
// any field-walking formatter and the marker never appears in output.
type secretBearingMsg struct{ token string }

func (s *secretBearingMsg) String() string { return "secret-token=" + s.token }

func newFakeCtx(id string) *fakeReceiverContext {
	return &fakeReceiverContext{self: &actor.PID{Address: "nonhost", Id: id}}
}

// decode parses the captured JSON stream into a slice of objects. Empty
// trailing lines are tolerated (slog ends every record with '\n').
func decodeLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("malformed json line %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

// invokeMiddleware applies the middleware to a no-op next and dispatches
// the message envelope. Returns the captured JSON-buffered lines.
func invokeMiddleware(t *testing.T, mw actor.ReceiverMiddleware, ctx actor.ReceiverContext, msg any) []map[string]any {
	t.Helper()
	// Replace slog.Default() so middlewares constructed without
	// WithLogger still funnel output through the buffer; tests using
	// WithLogger directly are unaffected.
	buf := logcapture.JSON(t, slog.LevelDebug)

	called := false
	next := func(_ actor.ReceiverContext, _ *actor.MessageEnvelope) { called = true }
	mw(next)(ctx, &actor.MessageEnvelope{Message: msg})
	if !called {
		t.Fatal("middleware did not call next")
	}
	return decodeLines(t, buf.String())
}

// TestGrainLogging_ExtractsClusterIdentityFromPartitionPID verifies the
// middleware's PID → cluster-identity reversal. protoactor-go's partition
// lookup activates a grain under PID
// "partition-activator/<identity>$<short>"; handler-side logs
// ctx.Identity() == "<identity>". The middleware must emit the same
// "<identity>" so operators can join lines on grain_id.
func TestGrainLogging_ExtractsClusterIdentityFromPartitionPID(t *testing.T) {
	mw := middleware.GrainLogging("RoomGrain")
	ctx := &fakeReceiverContext{self: &actor.PID{Address: "host", Id: "partition-activator/general$XK"}}
	lines := invokeMiddleware(t, mw, ctx, &actor.Started{})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0]["grain_id"] != "general" {
		t.Errorf("grain_id = %v, want general", lines[0]["grain_id"])
	}
}

func TestGrainLogging_FallsBackToFullPIDForUnknownScheme(t *testing.T) {
	mw := middleware.GrainLogging("RoomGrain")
	ctx := &fakeReceiverContext{self: &actor.PID{Address: "host", Id: "custom-scheme/whatever"}}
	lines := invokeMiddleware(t, mw, ctx, &actor.Started{})

	if lines[0]["grain_id"] != "custom-scheme/whatever" {
		t.Errorf("grain_id = %v, want full PID Id fallback", lines[0]["grain_id"])
	}
}

func TestGrainLogging_ActivatedOnStarted(t *testing.T) {
	mw := middleware.GrainLogging("RoomGrain")
	lines := invokeMiddleware(t, mw, newFakeCtx("general"), &actor.Started{})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %+v", len(lines), lines)
	}
	e := lines[0]
	if e["msg"] != "grain.activated" {
		t.Errorf("msg = %v, want grain.activated", e["msg"])
	}
	if e["grain_type"] != "RoomGrain" {
		t.Errorf("grain_type = %v, want RoomGrain", e["grain_type"])
	}
	if e["grain_id"] != "general" {
		t.Errorf("grain_id = %v, want general", e["grain_id"])
	}
	if e["msg_type"] != "actor.Started" {
		t.Errorf("msg_type = %v, want actor.Started", e["msg_type"])
	}
}

func TestGrainLogging_PassivatedOnStopping(t *testing.T) {
	mw := middleware.GrainLogging("UserGrain")
	lines := invokeMiddleware(t, mw, newFakeCtx("alice"), &actor.Stopping{})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0]["msg"] != "grain.passivated" {
		t.Errorf("msg = %v, want grain.passivated", lines[0]["msg"])
	}
	if lines[0]["grain_id"] != "alice" {
		t.Errorf("grain_id = %v, want alice", lines[0]["grain_id"])
	}
	if lines[0]["msg_type"] != "actor.Stopping" {
		t.Errorf("msg_type = %v, want actor.Stopping", lines[0]["msg_type"])
	}
}

func TestGrainLogging_LifecycleInfoForRestartingAndStopped(t *testing.T) {
	mw := middleware.GrainLogging("RoomGrain")

	for _, msg := range []any{&actor.Restarting{}, &actor.Stopped{}} {
		lines := invokeMiddleware(t, mw, newFakeCtx("general"), msg)
		if len(lines) != 1 {
			t.Fatalf("expected 1 info line for %T, got %d", msg, len(lines))
		}
		if lines[0]["msg"] != "grain.lifecycle" {
			t.Errorf("msg for %T = %v, want grain.lifecycle", msg, lines[0]["msg"])
		}
		if lines[0]["level"] != "INFO" {
			t.Errorf("level for %T = %v, want INFO", msg, lines[0]["level"])
		}
	}
}

func TestGrainLogging_GenericMessageLogsTypeName(t *testing.T) {
	mw := middleware.GrainLogging("RoomGrain")
	lines := invokeMiddleware(t, mw, newFakeCtx("general"), &roomTestMsg{Tag: "x"})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0]["msg"] != "grain.msg" {
		t.Errorf("msg = %v, want grain.msg", lines[0]["msg"])
	}
	got := lines[0]["msg_type"].(string)
	// fmt.Sprintf("%T", &roomTestMsg{}) → "*middleware_test.roomTestMsg";
	// the middleware trims the leading '*'.
	if !strings.HasSuffix(got, "middleware_test.roomTestMsg") || strings.HasPrefix(got, "*") {
		t.Errorf("msg_type = %v, want a non-pointer test-package type name", got)
	}
}

func TestGrainLogging_GrainRequestUsesEnvelopeName(t *testing.T) {
	mw := middleware.GrainLogging("UserGrain")
	lines := invokeMiddleware(t, mw, newFakeCtx("alice"), &cluster.GrainRequest{MethodIndex: 3, MessageData: []byte("ignored")})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0]["msg_type"] != "cluster.GrainRequest" {
		t.Errorf("msg_type = %v, want cluster.GrainRequest", lines[0]["msg_type"])
	}
	if _, present := lines[0]["method_index"]; present {
		t.Errorf("method_index should be absent from the envelope: %+v", lines[0])
	}
}

func TestGrainLogging_DoesNotLeakSecretFromMessageString(t *testing.T) {
	const marker = "super-secret-token-do-not-log"
	mw := middleware.GrainLogging("UserGrain")
	lines := invokeMiddleware(t, mw, newFakeCtx("alice"), &secretBearingMsg{token: marker})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	full, _ := json.Marshal(lines[0])
	if strings.Contains(string(full), marker) {
		t.Errorf("captured line leaked secret marker: %s", full)
	}
}

func TestActorLogging_OmitsUserIDWhenEmpty(t *testing.T) {
	mw := middleware.ActorLogging("UserConnection",
		middleware.WithUserIDProvider(func(actor.ReceiverContext) string { return "" }),
	)
	lines := invokeMiddleware(t, mw, newFakeCtx("$1"), &roomTestMsg{Tag: "preauth"})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if _, ok := lines[0]["user_id"]; ok {
		t.Errorf("user_id present despite empty provider return: %+v", lines[0])
	}
	if lines[0]["actor_type"] != "UserConnection" {
		t.Errorf("actor_type = %v, want UserConnection", lines[0]["actor_type"])
	}
	// PID.String() is proto-formatted (Address:"nonhost" Id:"$1") — match
	// what production connection.go logs as "pid", just under the
	// "actor_path" key here.
	path, _ := lines[0]["actor_path"].(string)
	if !strings.Contains(path, "nonhost") || !strings.Contains(path, "$1") {
		t.Errorf("actor_path = %q, expected to contain nonhost and $1", path)
	}
	if lines[0]["msg"] != "connection.msg" {
		t.Errorf("msg = %v, want connection.msg", lines[0]["msg"])
	}
}

func TestActorLogging_IncludesUserIDWhenSet(t *testing.T) {
	mw := middleware.ActorLogging("UserConnection",
		middleware.WithUserIDProvider(func(actor.ReceiverContext) string { return "alice" }),
	)
	lines := invokeMiddleware(t, mw, newFakeCtx("$1"), &roomTestMsg{Tag: "postauth"})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0]["user_id"] != "alice" {
		t.Errorf("user_id = %v, want alice", lines[0]["user_id"])
	}
}

func TestActorLogging_LifecycleAtInfo(t *testing.T) {
	mw := middleware.ActorLogging("UserConnection")
	lines := invokeMiddleware(t, mw, newFakeCtx("$1"), &actor.Started{})
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0]["msg"] != "connection.lifecycle" {
		t.Errorf("msg = %v, want connection.lifecycle", lines[0]["msg"])
	}
	if lines[0]["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", lines[0]["level"])
	}
}

// TestGrainLogging_WithLoggerRoutesOutput verifies WithLogger overrides
// slog.Default(). The previous default is left untouched.
func TestGrainLogging_WithLoggerRoutesOutput(t *testing.T) {
	buf := &bytes.Buffer{}
	dedicated := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Pin slog.Default() to a separate capture so we can tell which logger wrote.
	noopBuf := logcapture.JSON(t, slog.LevelInfo)

	mw := middleware.GrainLogging("RoomGrain", middleware.WithLogger(dedicated))
	next := func(_ actor.ReceiverContext, _ *actor.MessageEnvelope) {}
	mw(next)(newFakeCtx("general"), &actor.MessageEnvelope{Message: &actor.Started{}})

	if buf.Len() == 0 {
		t.Errorf("dedicated logger produced no output")
	}
	if noopBuf.String() != "" {
		t.Errorf("default logger received output despite WithLogger: %s", noopBuf.String())
	}
}

// TestGrainLogging_LifecycleLogsBeforeNextOnStopping pins the documented
// emission order. The middleware's contract is that a slow or panicking
// *actor.Stopping handler does not suppress the grain.passivated line —
// the line is in the buffer regardless of what next does.
func TestGrainLogging_LifecycleLogsBeforeNextOnStopping(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mw := middleware.GrainLogging("RoomGrain", middleware.WithLogger(logger))

	defer func() {
		// Swallow the simulated handler panic; the assertion below
		// proves the log line was emitted before it.
		_ = recover()
		lines := decodeLines(t, buf.String())
		if len(lines) != 1 {
			t.Fatalf("expected 1 lifecycle line before panic, got %d", len(lines))
		}
		if lines[0]["msg"] != "grain.passivated" {
			t.Errorf("msg = %v, want grain.passivated", lines[0]["msg"])
		}
	}()
	next := func(_ actor.ReceiverContext, _ *actor.MessageEnvelope) {
		panic(errors.New("simulated stopping handler panic"))
	}
	mw(next)(newFakeCtx("general"), &actor.MessageEnvelope{Message: &actor.Stopping{}})
}

// TestGrainLogging_OrdinaryMessageLogsAfterNext mirrors the
// "log-before-for-lifecycle, log-after-for-messages" contract: a panic
// during dispatch of an ordinary message suppresses the post-dispatch
// line. The supervisor's recovery is what surfaces the failure, not the
// middleware.
func TestGrainLogging_OrdinaryMessageLogsAfterNext(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mw := middleware.GrainLogging("RoomGrain", middleware.WithLogger(logger))

	defer func() {
		_ = recover()
		if buf.Len() != 0 {
			t.Errorf("expected no log line when next panics on ordinary msg, got: %s", buf.String())
		}
	}()
	next := func(_ actor.ReceiverContext, _ *actor.MessageEnvelope) {
		panic(errors.New("simulated handler panic"))
	}
	mw(next)(newFakeCtx("general"), &actor.MessageEnvelope{Message: &roomTestMsg{Tag: "x"}})
}

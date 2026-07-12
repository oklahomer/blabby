package connection

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/testutil/logcapture"
)

// These tests cover the connection side of the bidirectional watch (ADR-006):
// the grain activation's PID arrives in the RegisterConnection response, the
// actor watches it, and its death triggers a re-register. Each "activation"
// is a real spawned no-op actor whose PID travels through the fake caller, so
// a passing test proves ctx.Watch was genuinely armed — driving a synthetic
// *actor.Terminated through the mailbox would pass even without the watch.

func aliceAuthenticator() *stubAuthenticator {
	return &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
}

// spawnStubActivation spawns a no-op actor standing in for one User grain
// activation. Tests hand its PID to the connection through the fake caller
// and later poison it to produce a genuine death-watch Terminated.
func spawnStubActivation(t *testing.T, system *actor.ActorSystem) *actor.PID {
	t.Helper()
	pid := system.Root.Spawn(actor.PropsFromFunc(func(actor.Context) {}))
	t.Cleanup(func() { _ = system.Root.PoisonFuture(pid).Wait() })
	return pid
}

// enqueue appends replies to the caller's queue under its lock; the actor's
// mailbox goroutine reads the queue concurrently.
func (r *recordingGrainCaller) enqueue(replies ...registerReply) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queue = append(r.queue, replies...)
}

func respWithGrainPid(pid *actor.PID) *userpb.RegisterConnectionResponse {
	return &userpb.RegisterConnectionResponse{
		GrainPid: &userpb.PID{Address: pid.Address, Id: pid.Id},
	}
}

func authenticate(t *testing.T, sess *session) {
	t.Helper()
	writeAuthFrame(t, sess.client, "valid")
	if got := readJSON(t, sess.client); got["type"] != "auth_ok" {
		t.Fatalf("expected auth_ok, got %v", got)
	}
}

// assertDeliveryFlows drives a fan-out message through the actor and expects
// the corresponding frame on the still-open WebSocket.
func assertDeliveryFlows(t *testing.T, sess *session, text string) {
	t.Helper()
	sess.system.Root.Send(sess.pid, &userpb.ForwardMessageRequest{
		Room:   fanoutRoom(),
		Sender: &commonpb.UserRef{Id: "2", Name: "Bob Builder", PublicCode: "B000000002"},
		Text:   text,
	})
	if got := readJSON(t, sess.client); got["type"] != "message" || got["text"] != text {
		t.Fatalf("expected message %q on the open socket, got %v", text, got)
	}
}

// waitForLog polls the captured stream for substr; log lines are written by
// the actor's mailbox goroutine, so assertions on them must wait.
func waitForLog(t *testing.T, buf *logcapture.Buffer, substr string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !strings.Contains(buf.String(), substr) {
		if time.Now().After(deadline) {
			t.Fatalf("log event %q not observed within %s", substr, d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGrainTerminatedTriggersReregister(t *testing.T) {
	grain := &recordingGrainCaller{}
	sess := startSession(t, aliceAuthenticator(), grain)
	activation1 := spawnStubActivation(t, sess.system)
	activation2 := spawnStubActivation(t, sess.system)
	grain.enqueue(
		registerReply{resp: respWithGrainPid(activation1)}, // auth-time registration
		registerReply{resp: respWithGrainPid(activation2)}, // re-registration
	)

	authenticate(t, sess)
	_ = sess.system.Root.PoisonFuture(activation1).Wait()

	calls := grain.waitForCalls(t, 2, 2*time.Second)
	if calls[1].userID != "1" {
		t.Errorf("re-register userID: got %q, want 1", calls[1].userID)
	}
	if pid := calls[1].req.GetRequesterPid(); pid.GetAddress() == "" || pid.GetId() == "" {
		t.Errorf("re-register requester_pid must be populated, got %+v", pid)
	}
	// The session healed without a reconnect: deliveries still flow.
	assertDeliveryFlows(t, sess, "after-reactivation")
}

func TestRotatedGrainPIDIsWatched(t *testing.T) {
	grain := &recordingGrainCaller{}
	sess := startSession(t, aliceAuthenticator(), grain)
	activation1 := spawnStubActivation(t, sess.system)
	activation2 := spawnStubActivation(t, sess.system)
	activation3 := spawnStubActivation(t, sess.system)
	grain.enqueue(
		registerReply{resp: respWithGrainPid(activation1)},
		registerReply{resp: respWithGrainPid(activation2)},
		registerReply{resp: respWithGrainPid(activation3)},
	)

	authenticate(t, sess)

	// First death: re-register returns the rotated activation2 PID.
	_ = sess.system.Root.PoisonFuture(activation1).Wait()
	grain.waitForCalls(t, 2, 2*time.Second)

	// Second death, of the ROTATED PID. A third register proves the fresh
	// watch discipline: the actor watched activation2, not stale activation1.
	_ = sess.system.Root.PoisonFuture(activation2).Wait()
	grain.waitForCalls(t, 3, 2*time.Second)

	assertDeliveryFlows(t, sess, "after-second-rotation")
}

func TestReregisterExhaustionClosesConnection(t *testing.T) {
	grain := &recordingGrainCaller{}
	sess := startSession(t, aliceAuthenticator(), grain,
		WithReregisterRetryDelay(10*time.Millisecond))
	activation1 := spawnStubActivation(t, sess.system)
	grain.enqueue(
		registerReply{resp: respWithGrainPid(activation1)},
		registerReply{err: errors.New("boom-1")},
		registerReply{err: errors.New("boom-2")},
		registerReply{err: errors.New("boom-3")},
	)

	authenticate(t, sess)
	_ = sess.system.Root.PoisonFuture(activation1).Wait()

	// Auth registration plus three failed attempts, then the actor gives up
	// and closes so the client's reconnect becomes the fallback.
	grain.waitForCalls(t, 4, 2*time.Second)
	expectActorStops(t, sess.system, sess.pid, 2*time.Second)
}

func TestNilGrainPidAtAuthProceedsWithoutWatch(t *testing.T) {
	buf := logcapture.JSON(t, slog.LevelDebug)
	grain := &recordingGrainCaller{} // default reply carries no grain_pid
	sess := startSession(t, aliceAuthenticator(), grain)

	// Version skew: auth still succeeds, with a warning.
	authenticate(t, sess)
	waitForLog(t, buf, eventConnectionRegisterNoGrainPid, 2*time.Second)

	// Deliveries work; only the self-healing watch is missing.
	assertDeliveryFlows(t, sess, "degraded-but-working")
}

func TestNilGrainPidAtReregisterKeepsConnectionOpen(t *testing.T) {
	buf := logcapture.JSON(t, slog.LevelDebug)
	grain := &recordingGrainCaller{}
	sess := startSession(t, aliceAuthenticator(), grain)
	activation1 := spawnStubActivation(t, sess.system)
	grain.enqueue(
		registerReply{resp: respWithGrainPid(activation1)},
		registerReply{resp: &userpb.RegisterConnectionResponse{}}, // skewed re-register
	)

	authenticate(t, sess)
	_ = sess.system.Root.PoisonFuture(activation1).Wait()

	grain.waitForCalls(t, 2, 2*time.Second)
	waitForLog(t, buf, eventConnectionRegisterNoGrainPid, 2*time.Second)
	// Degraded success: registered, delivering, no retry burn-down or close.
	assertDeliveryFlows(t, sess, "degraded-after-skew")
}

package connection

import (
	"context"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/auth"
)

func TestReadPumpPanicStopsConnectionActor(t *testing.T) {
	sess := startSession(t, unusedAuthenticator(t), &recordingGrainCaller{})

	sess.system.Root.Send(sess.pid, &ReadPumpPanicked{Cause: "test panic"})

	expectActorStops(t, sess.system, sess.pid, time.Second)
}

func TestWritePumpFailureStopsConnectionActor(t *testing.T) {
	sess := startSession(t, unusedAuthenticator(t), &recordingGrainCaller{})

	sess.system.Root.Send(sess.pid, &WritePumpFailed{
		Reason:    "write_error",
		EventKind: "message",
	})

	expectActorStops(t, sess.system, sess.pid, time.Second)
}

func TestAuthTimeoutAfterAuthenticationIsIgnored(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{})
	writeAuthFrame(t, sess.client, "valid")
	if got := readJSON(t, sess.client); got["type"] != "auth_ok" {
		t.Fatalf("expected auth_ok, got %v", got)
	}

	sess.system.Root.Send(sess.pid, &AuthTimeoutExpired{})
	sess.system.Root.Send(sess.pid, &userpb.ForwardMessageRequest{
		RoomId: "4",
		Sender: &commonpb.UserRef{Id: "bob", Name: "Bob Builder"},
		Text:   "still-connected",
	})

	if got := readJSON(t, sess.client); got["type"] != "message" || got["text"] != "still-connected" {
		t.Fatalf("expected connection to remain active after stale auth timeout, got %v", got)
	}
}

func TestOutboundBackpressureStopsActor(t *testing.T) {
	system := actor.NewActorSystem()
	uc := &UserConnection{outbound: make(chan any, 1)}
	uc.outbound <- &AppPing{}

	props := actor.PropsFromFunc(func(ctx actor.Context) {
		if _, ok := ctx.Message().(*triggerOutboundBackpressure); ok {
			uc.sendOutbound(ctx, &AppPing{})
		}
	}, actor.WithGuardian(connectionSupervisor))
	pid := system.Root.Spawn(props)
	t.Cleanup(func() { _ = system.Root.PoisonFuture(pid).Wait() })

	system.Root.Send(pid, &triggerOutboundBackpressure{})

	expectActorStops(t, system, pid, time.Second)
}

type triggerOutboundBackpressure struct{}

func unusedAuthenticator(t *testing.T) *stubAuthenticator {
	t.Helper()
	return &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		t.Fatal("authenticator must not run on a transport failure path")
		return nil, nil
	}}
}

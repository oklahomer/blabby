package graintest

import (
	"testing"

	"github.com/asynkron/protoactor-go/actor"
)

func TestNewFakeGrainContext_ReportsIdentityAndKind(t *testing.T) {
	ctx := NewFakeGrainContext("general")

	if got := ctx.Identity(); got != "general" {
		t.Errorf("Identity: got %q, want %q", got, "general")
	}
	if got := ctx.Kind(); got != "" {
		t.Errorf("Kind: got %q, want empty (callers opt in via WithKind)", got)
	}
	if ctx.Cluster() != nil {
		t.Errorf("Cluster: got non-nil, want nil")
	}
}

func TestNewFakeGrainContext_WithKindOverridesDefault(t *testing.T) {
	ctx := NewFakeGrainContext("general", WithKind("RoomGrain"))

	if got := ctx.Kind(); got != "RoomGrain" {
		t.Errorf("Kind: got %q, want %q", got, "RoomGrain")
	}
}

func TestNewFakeGrainContext_PanicsOnUnsupportedActorOps(t *testing.T) {
	ctx := NewFakeGrainContext("any")

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on unsupported actor.Context call, got nil")
		}
	}()
	_ = ctx.ActorSystem() // backed by nil actor.Context — must panic
}

func TestNewFakeGrainContext_DefaultSelf(t *testing.T) {
	ctx := NewFakeGrainContext("general")

	self := ctx.Self()
	if self.GetAddress() != defaultSelfAddress || self.GetId() != "general" {
		t.Errorf("Self: got %v, want %s/general", self, defaultSelfAddress)
	}
}

func TestFakeGrainContext_Options(t *testing.T) {
	sender := actor.NewPID("test", "sender-1")
	self := actor.NewPID("test", "activation-1")
	rec := &WatchRecorder{}
	ctx := NewFakeGrainContext("room-1",
		WithKind("RoomGrain"),
		WithSender(sender),
		WithSelf(self),
		WithMessage("hello"),
		WithWatchRecorder(rec),
	)

	if ctx.Sender() != sender {
		t.Errorf("Sender: got %v, want %v", ctx.Sender(), sender)
	}
	if ctx.Self() != self {
		t.Errorf("Self: got %v, want %v", ctx.Self(), self)
	}
	if got := ctx.Message(); got != "hello" {
		t.Errorf("Message: got %v, want %q", got, "hello")
	}

	// Watch is recorded through the recorder; Unwatch is a no-op that must
	// not reach the nil embedded actor.Context (it would panic).
	watched := actor.NewPID("test", "watched-1")
	ctx.Watch(watched)
	ctx.Unwatch(watched)
	pids := rec.PIDs()
	if len(pids) != 1 || pids[0] != watched {
		t.Fatalf("WatchRecorder.PIDs: got %v, want [%v]", pids, watched)
	}
}

func TestWatch_WithoutRecorderIsNoOp(t *testing.T) {
	ctx := NewFakeGrainContext("room-1")
	// No recorder attached: Watch must be a silent no-op and must not reach
	// the nil embedded actor.Context.
	ctx.Watch(actor.NewPID("test", "x"))
}

func TestNewFakeGrainContextWithMessage(t *testing.T) {
	ctx := NewFakeGrainContextWithMessage("room-1", 42)
	if got := ctx.Message(); got != 42 {
		t.Errorf("Message: got %v, want 42", got)
	}
	if got := ctx.Identity(); got != "room-1" {
		t.Errorf("Identity: got %q, want %q", got, "room-1")
	}
}

func TestRequestConstructors(t *testing.T) {
	if got := NewJoinRequest("alice"); got.GetUser().GetId() != "alice" {
		t.Errorf("NewJoinRequest user id: got %q, want %q", got.GetUser().GetId(), "alice")
	}
	if got := NewJoinRequest("alice"); got.GetUser().GetName() != "alice" {
		t.Errorf("NewJoinRequest user name: got %q, want %q (defaults to id)", got.GetUser().GetName(), "alice")
	}
	got := NewPostMessageRequest("carol", "hello")
	if got.GetUser().GetId() != "carol" || got.GetText() != "hello" {
		t.Errorf("NewPostMessageRequest: got (%q, %q), want (%q, %q)",
			got.GetUser().GetId(), got.GetText(), "carol", "hello")
	}
	if got.GetUser().GetName() != "carol" {
		t.Errorf("NewPostMessageRequest user name: got %q, want %q (defaults to id)", got.GetUser().GetName(), "carol")
	}
}

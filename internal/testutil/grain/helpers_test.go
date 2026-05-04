package graintest

import "testing"

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
	_ = ctx.Self() // backed by nil actor.Context — must panic
}

func TestRequestConstructors(t *testing.T) {
	if got := NewJoinRequest("alice"); got.GetUserId() != "alice" {
		t.Errorf("NewJoinRequest user_id: got %q, want %q", got.GetUserId(), "alice")
	}
	if got := NewLeaveRequest("bob"); got.GetUserId() != "bob" {
		t.Errorf("NewLeaveRequest user_id: got %q, want %q", got.GetUserId(), "bob")
	}
	got := NewPostMessageRequest("carol", "hello")
	if got.GetUserId() != "carol" || got.GetText() != "hello" {
		t.Errorf("NewPostMessageRequest: got (%q, %q), want (%q, %q)",
			got.GetUserId(), got.GetText(), "carol", "hello")
	}
}

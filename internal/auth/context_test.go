package auth_test

import (
	"context"
	"testing"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/id"
)

func mustUserID(t *testing.T, raw string) id.UserID {
	t.Helper()
	uid, err := id.NewUserID(raw)
	if err != nil {
		t.Fatalf("mustUserID(%q): %v", raw, err)
	}
	return uid
}

func TestContextWithUserID_RoundTrip(t *testing.T) {
	want := mustUserID(t, "user-123")
	ctx := auth.ContextWithUserID(context.Background(), want)

	got, ok := auth.UserIDFromContext(ctx)
	if !ok {
		t.Fatal("UserIDFromContext returned ok=false after ContextWithUserID")
	}
	if got != want {
		t.Errorf("got %q, want %q", got.String(), want.String())
	}
}

func TestUserIDFromContext_MissingKey(t *testing.T) {
	got, ok := auth.UserIDFromContext(context.Background())
	if ok {
		t.Errorf("expected ok=false on missing key, got ok=true with value %q", got.String())
	}
	if got != (id.UserID{}) {
		t.Errorf("expected zero UserID on missing key, got %q", got.String())
	}
}

func TestContextWithUserID_ZeroValue(t *testing.T) {
	// A zero-value UserID is stored and returned as-is — do not silently
	// coerce. Production code never stores the zero value (the middleware
	// only injects a parsed value), but the round-trip property is part
	// of the API contract.
	ctx := auth.ContextWithUserID(context.Background(), id.UserID{})
	got, ok := auth.UserIDFromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true even when userID is zero (caller stored it explicitly)")
	}
	if got != (id.UserID{}) {
		t.Errorf("got %q, want zero UserID", got.String())
	}
}

func TestUserIDFromContext_WrongTypedValue(t *testing.T) {
	// A foreign key — even one whose underlying type matches — must not
	// collide with auth's unexported context key. We simulate that by storing
	// a value under a different key type and asserting we cannot retrieve it.
	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, mustUserID(t, "user-evil"))

	got, ok := auth.UserIDFromContext(ctx)
	if ok {
		t.Errorf("expected ok=false for value stored under foreign key, got ok=true with %q", got.String())
	}
	if got != (id.UserID{}) {
		t.Errorf("expected zero UserID, got %q", got.String())
	}
}

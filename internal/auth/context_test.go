package auth_test

import (
	"context"
	"testing"

	"github.com/oklahomer/blabby/internal/auth"
)

func TestContextWithUserID_RoundTrip(t *testing.T) {
	ctx := context.Background()
	ctx = auth.ContextWithUserID(ctx, "user-123")

	got, ok := auth.UserIDFromContext(ctx)
	if !ok {
		t.Fatal("UserIDFromContext returned ok=false after ContextWithUserID")
	}
	if got != "user-123" {
		t.Errorf("got %q, want %q", got, "user-123")
	}
}

func TestUserIDFromContext_MissingKey(t *testing.T) {
	got, ok := auth.UserIDFromContext(context.Background())
	if ok {
		t.Errorf("expected ok=false on missing key, got ok=true with value %q", got)
	}
	if got != "" {
		t.Errorf("expected empty string on missing key, got %q", got)
	}
}

func TestContextWithUserID_EmptyValue(t *testing.T) {
	// Empty userID is stored and returned as-is — do not silently coerce.
	ctx := auth.ContextWithUserID(context.Background(), "")
	got, ok := auth.UserIDFromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true even when userID is empty (caller stored it explicitly)")
	}
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestUserIDFromContext_WrongTypedValue(t *testing.T) {
	// A foreign key — even one whose underlying type is string — must not
	// collide with auth's unexported context key. We simulate that by storing
	// a value under a different key type and asserting we cannot retrieve it.
	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, "user-evil")

	got, ok := auth.UserIDFromContext(ctx)
	if ok {
		t.Errorf("expected ok=false for value stored under foreign key, got ok=true with %q", got)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

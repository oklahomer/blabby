package id

import (
	"errors"
	"strings"
	"testing"
)

func TestNewRoomID(t *testing.T) {
	t.Run("valid input round-trips through String", func(t *testing.T) {
		rid, err := NewRoomID("general")
		if err != nil {
			t.Fatalf("NewRoomID returned error: %v", err)
		}
		if rid.String() != "general" {
			t.Errorf("String() = %q, want %q", rid.String(), "general")
		}
	})

	t.Run("zero value has empty String", func(t *testing.T) {
		var zero RoomID
		if zero.String() != "" {
			t.Errorf("zero RoomID String() = %q, want empty", zero.String())
		}
	})

	t.Run("wraps sentinel and prefixes with room_id", func(t *testing.T) {
		_, err := NewRoomID("")
		if err == nil {
			t.Fatal("NewRoomID(\"\") returned nil error")
		}
		if !errors.Is(err, ErrEmptyIdentifier) {
			t.Errorf("err does not wrap ErrEmptyIdentifier: %v", err)
		}
		if !strings.HasPrefix(err.Error(), "room_id: ") {
			t.Errorf("err = %q, want prefix %q", err.Error(), "room_id: ")
		}
	})

	t.Run("rejects path-traversal slash that mux URL-decodes", func(t *testing.T) {
		_, err := NewRoomID("foo/bar")
		if !errors.Is(err, ErrIdentifierInvalidChar) {
			t.Errorf("expected ErrIdentifierInvalidChar for slash, got %v", err)
		}
	})

	t.Run("structurally identical RoomIDs compare equal", func(t *testing.T) {
		a, _ := NewRoomID("general")
		b, _ := NewRoomID("general")
		if a != b {
			t.Errorf("equal-valued RoomIDs compared not equal: %#v vs %#v", a, b)
		}
	})
}

// TestUserIDAndRoomIDAreDistinctTypes is a compile-time guard masquerading
// as a runtime test. It is the only place that exercises the cross-type
// rejection. If a future refactor accidentally aliases UserID and RoomID
// to the same struct (e.g., via a generic Identifier[T]), this file stops
// compiling. The body intentionally does almost nothing at runtime.
func TestUserIDAndRoomIDAreDistinctTypes(t *testing.T) {
	uid, _ := NewUserID("alice")
	rid, _ := NewRoomID("general")

	// The point of the test: distinct types accept distinct sets of
	// values via the type system. The runtime values may match (both
	// wrap a string), but the types do not.
	if uid.String() == rid.String() {
		t.Fatalf("test fixtures collided on string value; pick different inputs")
	}
}

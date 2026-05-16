package ids

import (
	"errors"
	"strings"
	"testing"
)

func TestNewUserID(t *testing.T) {
	t.Run("valid input round-trips through String", func(t *testing.T) {
		uid, err := NewUserID("alice")
		if err != nil {
			t.Fatalf("NewUserID returned error: %v", err)
		}
		if uid.String() != "alice" {
			t.Errorf("String() = %q, want %q", uid.String(), "alice")
		}
	})

	t.Run("zero value has empty String", func(t *testing.T) {
		var zero UserID
		if zero.String() != "" {
			t.Errorf("zero UserID String() = %q, want empty", zero.String())
		}
	})

	t.Run("wraps sentinel and prefixes with user_id", func(t *testing.T) {
		_, err := NewUserID("")
		if err == nil {
			t.Fatal("NewUserID(\"\") returned nil error")
		}
		if !errors.Is(err, ErrEmptyIdentifier) {
			t.Errorf("err does not wrap ErrEmptyIdentifier: %v", err)
		}
		if !strings.HasPrefix(err.Error(), "user_id: ") {
			t.Errorf("err = %q, want prefix %q", err.Error(), "user_id: ")
		}
	})

	t.Run("structurally identical UserIDs compare equal", func(t *testing.T) {
		a, _ := NewUserID("alice")
		b, _ := NewUserID("alice")
		if a != b {
			t.Errorf("equal-valued UserIDs compared not equal: %#v vs %#v", a, b)
		}
	})

	t.Run("UserIDs from different inputs compare unequal", func(t *testing.T) {
		a, _ := NewUserID("alice")
		b, _ := NewUserID("bob")
		if a == b {
			t.Errorf("distinct UserIDs compared equal: %#v vs %#v", a, b)
		}
	})

	t.Run("failure returns zero value", func(t *testing.T) {
		got, err := NewUserID("foo/bar")
		if err == nil {
			t.Fatal("expected error for invalid input")
		}
		var zero UserID
		if got != zero {
			t.Errorf("failed NewUserID returned %#v, want zero value", got)
		}
	})
}
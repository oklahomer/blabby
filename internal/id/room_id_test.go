package id

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNewRoomID(t *testing.T) {
	t.Run("positive value round-trips through Int64 and String", func(t *testing.T) {
		rid, err := NewRoomID(4)
		if err != nil {
			t.Fatalf("NewRoomID returned error: %v", err)
		}
		if rid.Int64() != 4 {
			t.Errorf("Int64() = %d, want 4", rid.Int64())
		}
		if rid.String() != "4" {
			t.Errorf("String() = %q, want %q", rid.String(), "4")
		}
	})

	t.Run("rejects non-positive values", func(t *testing.T) {
		for _, v := range []int64{0, -1} {
			got, err := NewRoomID(v)
			if !errors.Is(err, ErrInvalidRoomID) {
				t.Errorf("NewRoomID(%d) err = %v, want ErrInvalidRoomID", v, err)
			}
			if got != (RoomID{}) {
				t.Errorf("NewRoomID(%d) returned %#v, want zero value", v, got)
			}
		}
	})

	t.Run("structurally identical RoomIDs compare equal", func(t *testing.T) {
		a, _ := NewRoomID(4)
		b, _ := NewRoomID(4)
		if a != b {
			t.Errorf("equal-valued RoomIDs compared not equal: %#v vs %#v", a, b)
		}
	})
}

func TestRoomID_IsZero(t *testing.T) {
	if !(RoomID{}).IsZero() {
		t.Error("IsZero() = false for the zero value, want true")
	}
	rid, err := NewRoomID(4)
	if err != nil {
		t.Fatalf("NewRoomID: %v", err)
	}
	if rid.IsZero() {
		t.Error("IsZero() = true for a constructed id, want false")
	}
}

func TestParseRoomID(t *testing.T) {
	t.Run("decimal string parses to the same value", func(t *testing.T) {
		rid, err := ParseRoomID("5")
		if err != nil {
			t.Fatalf("ParseRoomID returned error: %v", err)
		}
		if rid.Int64() != 5 {
			t.Errorf("Int64() = %d, want 5", rid.Int64())
		}
	})

	t.Run("rejects non-numeric and non-positive input", func(t *testing.T) {
		for _, s := range []string{"", "general", "foo/bar", "0", "-1"} {
			if _, err := ParseRoomID(s); err == nil {
				t.Errorf("ParseRoomID(%q) = nil error, want failure", s)
			}
		}
	})
}

func TestRoomID_JSONRoundTrip(t *testing.T) {
	rid, _ := NewRoomID(7240534144614405)

	data, err := json.Marshal(rid)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `"7240534144614405"` {
		t.Errorf("Marshal = %s, want a decimal string", data)
	}

	var got RoomID
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != rid {
		t.Errorf("round-trip = %v, want %v", got, rid)
	}
}

// TestUserIDAndRoomIDAreDistinctTypes is a compile-time guard masquerading as a
// runtime test. If a future refactor accidentally aliases UserID and RoomID to
// the same struct (e.g., via a generic Identifier[T]), this file stops compiling.
func TestUserIDAndRoomIDAreDistinctTypes(t *testing.T) {
	uid, _ := NewUserID(1)
	rid, _ := NewRoomID(1)

	// The runtime values may match (both wrap an int64), but the types do not,
	// so no function accepting one will accept the other.
	if uid.Int64() != rid.Int64() {
		t.Fatalf("test fixtures diverged; both should wrap the same int64")
	}
}

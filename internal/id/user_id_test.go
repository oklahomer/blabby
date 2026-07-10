package id

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNewUserID(t *testing.T) {
	t.Run("positive value round-trips through Int64 and String", func(t *testing.T) {
		uid, err := NewUserID(42)
		if err != nil {
			t.Fatalf("NewUserID returned error: %v", err)
		}
		if uid.Int64() != 42 {
			t.Errorf("Int64() = %d, want 42", uid.Int64())
		}
		if uid.String() != "42" {
			t.Errorf("String() = %q, want %q", uid.String(), "42")
		}
	})

	t.Run("rejects non-positive values", func(t *testing.T) {
		for _, v := range []int64{0, -1} {
			got, err := NewUserID(v)
			if !errors.Is(err, ErrInvalidUserID) {
				t.Errorf("NewUserID(%d) err = %v, want ErrInvalidUserID", v, err)
			}
			if got != (UserID{}) {
				t.Errorf("NewUserID(%d) returned %#v, want zero value", v, got)
			}
		}
	})

	t.Run("structurally identical UserIDs compare equal", func(t *testing.T) {
		a, _ := NewUserID(7)
		b, _ := NewUserID(7)
		if a != b {
			t.Errorf("equal-valued UserIDs compared not equal: %#v vs %#v", a, b)
		}
	})

	t.Run("UserIDs from different inputs compare unequal", func(t *testing.T) {
		a, _ := NewUserID(7)
		b, _ := NewUserID(8)
		if a == b {
			t.Errorf("distinct UserIDs compared equal: %#v vs %#v", a, b)
		}
	})
}

func TestUserID_IsZero(t *testing.T) {
	if !(UserID{}).IsZero() {
		t.Error("IsZero() = false for the zero value, want true")
	}
	uid, err := NewUserID(42)
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	if uid.IsZero() {
		t.Error("IsZero() = true for a constructed id, want false")
	}
}

func TestParseUserID(t *testing.T) {
	t.Run("decimal string parses to the same value", func(t *testing.T) {
		uid, err := ParseUserID("123")
		if err != nil {
			t.Fatalf("ParseUserID returned error: %v", err)
		}
		if uid.Int64() != 123 {
			t.Errorf("Int64() = %d, want 123", uid.Int64())
		}
	})

	t.Run("round-trips through String", func(t *testing.T) {
		uid, _ := NewUserID(99)
		got, err := ParseUserID(uid.String())
		if err != nil || got != uid {
			t.Errorf("round-trip: got %v err %v, want %v", got, err, uid)
		}
	})

	t.Run("rejects non-numeric and non-positive input", func(t *testing.T) {
		for _, s := range []string{"", "abc", "1.5", "0", "-3", "9223372036854775808"} {
			if _, err := ParseUserID(s); err == nil {
				t.Errorf("ParseUserID(%q) = nil error, want failure", s)
			}
		}
	})
}

func TestUserID_JSONRoundTrip(t *testing.T) {
	uid, _ := NewUserID(7240534144614400)

	data, err := json.Marshal(uid)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `"7240534144614400"` {
		t.Errorf("Marshal = %s, want a decimal string", data)
	}

	var got UserID
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != uid {
		t.Errorf("round-trip = %v, want %v", got, uid)
	}

	if err := json.Unmarshal([]byte("7240534144614400"), &got); err == nil {
		t.Error("Unmarshal of a bare JSON number = nil error, want rejection")
	}
}

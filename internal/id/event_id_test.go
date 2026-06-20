package id

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNewEventID(t *testing.T) {
	for _, v := range []int64{0, -1, -9999} {
		if _, err := NewEventID(v); !errors.Is(err, ErrInvalidEventID) {
			t.Errorf("NewEventID(%d): got %v, want ErrInvalidEventID", v, err)
		}
	}
	e, err := NewEventID(123)
	if err != nil {
		t.Fatalf("NewEventID(123): %v", err)
	}
	if e.Int64() != 123 {
		t.Errorf("Int64() = %d, want 123", e.Int64())
	}
	if e.String() != "123" {
		t.Errorf("String() = %q, want \"123\"", e.String())
	}
}

func TestEventIDMarshalsAsDecimalString(t *testing.T) {
	// A 63-bit-ish value that loses precision as a JSON number.
	e, err := NewEventID(9007199254740993)
	if err != nil {
		t.Fatalf("NewEventID: %v", err)
	}
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `"9007199254740993"`; string(got) != want {
		t.Fatalf("Marshal = %s, want %s", got, want)
	}
}

func TestEventIDUnmarshal(t *testing.T) {
	t.Run("round trips through a struct field", func(t *testing.T) {
		type wrap struct {
			ID EventID `json:"id"`
		}
		in := wrap{ID: mustEventID(t, 9007199254740993)}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var out wrap
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if out.ID != in.ID {
			t.Fatalf("round trip = %v, want %v", out.ID, in.ID)
		}
	})

	t.Run("rejects a bare JSON number", func(t *testing.T) {
		var e EventID
		if err := json.Unmarshal([]byte("123"), &e); err == nil {
			t.Fatal("expected error for numeric form, got nil")
		}
	})

	t.Run("rejects a non-positive string", func(t *testing.T) {
		var e EventID
		if err := json.Unmarshal([]byte(`"0"`), &e); !errors.Is(err, ErrInvalidEventID) {
			t.Fatalf("got %v, want ErrInvalidEventID", err)
		}
	})

	t.Run("rejects a non-numeric string", func(t *testing.T) {
		var e EventID
		if err := json.Unmarshal([]byte(`"abc"`), &e); err == nil {
			t.Fatal("expected error for non-numeric string, got nil")
		}
	})
}

func mustEventID(t *testing.T, v int64) EventID {
	t.Helper()
	e, err := NewEventID(v)
	if err != nil {
		t.Fatalf("NewEventID(%d): %v", v, err)
	}
	return e
}

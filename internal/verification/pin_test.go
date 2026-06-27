package verification

import (
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestNewPIN_FormatAndVariety(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 200; i++ {
		pin, err := NewPIN()
		if err != nil {
			t.Fatalf("NewPIN: %v", err)
		}
		s := pin.String()
		if len(s) != pinDigits {
			t.Fatalf("PIN %q length = %d, want %d", s, len(s), pinDigits)
		}
		for _, r := range s {
			if r < '0' || r > '9' {
				t.Fatalf("PIN %q contains a non-digit", s)
			}
		}
		seen[s] = struct{}{}
	}
	// Not a statistical test — just a sanity check that generation is not constant.
	if len(seen) < 100 {
		t.Errorf("200 PINs produced only %d distinct values; generation looks degenerate", len(seen))
	}
}

func TestParsePIN(t *testing.T) {
	if p, err := ParsePIN("  012345 "); err != nil || p.String() != "012345" {
		t.Errorf("ParsePIN(padded) = %q, %v; want 012345, nil", p.String(), err)
	}
	for _, raw := range []string{"", "12345", "1234567", "12a456", "abcdef", "  12 45"} {
		if _, err := ParsePIN(raw); !errors.Is(err, ErrInvalidPIN) {
			t.Errorf("ParsePIN(%q) err = %v, want ErrInvalidPIN", raw, err)
		}
	}
}

func TestHashAndVerify_RoundTrip(t *testing.T) {
	pin, err := NewPIN()
	if err != nil {
		t.Fatalf("NewPIN: %v", err)
	}
	hash, err := pin.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if err := Verify(hash, pin.String()); err != nil {
		t.Errorf("Verify(correct) = %v, want nil", err)
	}

	// A different, well-formed PIN must not verify.
	wrong := "000000"
	if pin.String() == wrong {
		wrong = "111111"
	}
	if err := Verify(hash, wrong); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Errorf("Verify(wrong) = %v, want a bcrypt mismatch", err)
	}

	// A malformed submission is rejected before the hash comparison.
	if err := Verify(hash, "12ab"); !errors.Is(err, ErrInvalidPIN) {
		t.Errorf("Verify(malformed) = %v, want ErrInvalidPIN", err)
	}
}

func TestHash_RejectsZeroValue(t *testing.T) {
	if _, err := (PIN{}).Hash(); !errors.Is(err, ErrInvalidPIN) {
		t.Fatalf("zero-value Hash err = %v, want ErrInvalidPIN", err)
	}
}

func TestHash_RejectsNonCanonicalValue(t *testing.T) {
	if _, err := (PIN{value: " 123456 "}).Hash(); !errors.Is(err, ErrInvalidPIN) {
		t.Fatalf("non-canonical Hash err = %v, want ErrInvalidPIN", err)
	}
}

func TestHash_Cost(t *testing.T) {
	pin, err := NewPIN()
	if err != nil {
		t.Fatalf("NewPIN: %v", err)
	}
	hash, err := pin.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	cost, err := bcrypt.Cost(hash)
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost != pinHashCost {
		t.Errorf("cost = %d, want %d", cost, pinHashCost)
	}
}

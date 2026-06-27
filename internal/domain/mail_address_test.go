package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestNewMailAddress_NormalizesAndAccepts(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "already normalized", raw: "alice@example.com", want: "alice@example.com"},
		{name: "uppercased", raw: "Alice@Example.COM", want: "alice@example.com"},
		{name: "surrounding whitespace", raw: "  bob@example.com\t", want: "bob@example.com"},
		{name: "plus addressing", raw: "carol+tag@example.com", want: "carol+tag@example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.NewMailAddress(tc.raw)
			if err != nil {
				t.Fatalf("NewMailAddress(%q): %v", tc.raw, err)
			}
			if got.String() != tc.want {
				t.Errorf("String() = %q, want %q", got.String(), tc.want)
			}
		})
	}
}

func TestNewMailAddress_Rejects(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"whitespace only":   "   ",
		"no domain":         "alice",
		"no local part":     "@example.com",
		"display-name form": "Alice <alice@example.com>",
		"two addresses":     "a@example.com, b@example.com",
		"trailing text":     "alice@example.com junk",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := domain.NewMailAddress(raw); !errors.Is(err, domain.ErrInvalidMailAddress) {
				t.Errorf("NewMailAddress(%q) err = %v, want ErrInvalidMailAddress", raw, err)
			}
		})
	}
}

func TestNewMailAddress_RejectsOverLength(t *testing.T) {
	// A local part long enough to push the whole address past the 254-byte cap.
	raw := strings.Repeat("a", domain.MaxMailAddressBytes-len("@example.com")+1) + "@example.com"
	if _, err := domain.NewMailAddress(raw); !errors.Is(err, domain.ErrInvalidMailAddress) {
		t.Errorf("NewMailAddress(len %d) err = %v, want ErrInvalidMailAddress", len(raw), err)
	}
}

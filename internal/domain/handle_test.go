package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestNewHandle_AcceptsAndNormalizes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		raw         string
		wantDisplay string
		wantNorm    string
	}{
		{name: "lowercase", raw: "alice", wantDisplay: "alice", wantNorm: "alice"},
		{name: "mixed case kept for display, lowered for norm", raw: "Alice_99", wantDisplay: "Alice_99", wantNorm: "alice_99"},
		{name: "surrounding whitespace trimmed", raw: "  bob_x  ", wantDisplay: "bob_x", wantNorm: "bob_x"},
		{name: "digits and underscores", raw: "u_2_0", wantDisplay: "u_2_0", wantNorm: "u_2_0"},
		{name: "min length", raw: "abc", wantDisplay: "abc", wantNorm: "abc"},
		{name: "max length", raw: strings.Repeat("a", 30), wantDisplay: strings.Repeat("a", 30), wantNorm: strings.Repeat("a", 30)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, err := domain.NewHandle(tc.raw)
			if err != nil {
				t.Fatalf("NewHandle(%q): %v", tc.raw, err)
			}
			if h.Display() != tc.wantDisplay {
				t.Errorf("Display() = %q, want %q", h.Display(), tc.wantDisplay)
			}
			if h.Normalized() != tc.wantNorm {
				t.Errorf("Normalized() = %q, want %q", h.Normalized(), tc.wantNorm)
			}
		})
	}
}

func TestNewHandle_Rejects(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"empty":        "",
		"too short":    "ab",
		"too long":     strings.Repeat("a", 31),
		"hyphen":       "ab-cd",
		"dot":          "ab.cd",
		"space inside": "ab cd",
		"at sign":      "ab@cd",
		"non-ascii":    "abçd",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := domain.NewHandle(raw); !errors.Is(err, domain.ErrInvalidHandle) {
				t.Errorf("NewHandle(%q) err = %v, want ErrInvalidHandle", raw, err)
			}
		})
	}
}

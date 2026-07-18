package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestNewRoomNameQuery(t *testing.T) {
	t.Parallel()
	t.Run("accepts and trims", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
			want string
		}{
			{"plain", "General", "General"},
			{"single rune", "G", "G"},
			{"surrounding whitespace trimmed", "  Random \t", "Random"},
			{"spaces inside kept", "Team Standup", "Team Standup"},
			{"cjk", "雑談", "雑談"},
			{"emoji", "🎉", "🎉"},
			{"like wildcards are ordinary text", "100%_done\\", "100%_done\\"},
			// NFD input composes to the NFC form room names are stored in.
			{"nfd composed to nfc", "が", "が"},
			{"max bytes", strings.Repeat("a", domain.MaxRoomNameBytes), strings.Repeat("a", domain.MaxRoomNameBytes)},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, err := domain.NewRoomNameQuery(tc.raw)
				if err != nil {
					t.Fatalf("NewRoomNameQuery(%q): %v", tc.raw, err)
				}
				if got.String() != tc.want {
					t.Errorf("String() = %q, want %q", got.String(), tc.want)
				}
				if got.IsZero() {
					t.Errorf("IsZero() = true for constructed query %q", tc.raw)
				}
			})
		}
	})

	t.Run("rejects", func(t *testing.T) {
		cases := map[string]string{
			"empty":          "",
			"blank":          "   ",
			"over max bytes": strings.Repeat("a", domain.MaxRoomNameBytes+1),
			// The same character rules as RoomName: a fragment containing a rune
			// that cannot appear in any display name can never match one.
			"NUL":              "ro\x00om",
			"newline inside":   "line\nbreak",
			"ansi escape":      "evil\x1b[31mred",
			"zero-width space": "sneaky\u200bname",
			"bidi override":    "abc\u202edef",
			"invalid utf-8":    "bad\xffbyte",
			"line separator":   "a\u2028b",
		}
		for name, raw := range cases {
			t.Run(name, func(t *testing.T) {
				if _, err := domain.NewRoomNameQuery(raw); !errors.Is(err, domain.ErrInvalidRoomNameQuery) {
					t.Errorf("NewRoomNameQuery(%q) err = %v, want ErrInvalidRoomNameQuery", raw, err)
				}
			})
		}
	})

	t.Run("zero value is zero", func(t *testing.T) {
		var q domain.RoomNameQuery
		if !q.IsZero() {
			t.Error("IsZero() = false for the zero value")
		}
		if q.String() != "" {
			t.Errorf("String() = %q for the zero value, want empty", q.String())
		}
	})
}

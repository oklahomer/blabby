package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestNewRoomName(t *testing.T) {
	t.Parallel()
	t.Run("accepts and trims", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
			want string
		}{
			{"plain", "General", "General"},
			{"surrounding whitespace trimmed", "  Random \t", "Random"},
			{"spaces inside kept", "Team Standup", "Team Standup"},
			{"cjk", "雑談部屋", "雑談部屋"},
			{"ideographic space inside kept", "雑談　部屋", "雑談　部屋"},
			{"emoji", "🎉 party", "🎉 party"},
			{"max bytes", strings.Repeat("a", domain.MaxRoomNameBytes), strings.Repeat("a", domain.MaxRoomNameBytes)},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, err := domain.NewRoomName(tc.raw)
				if err != nil {
					t.Fatalf("NewRoomName(%q): %v", tc.raw, err)
				}
				if got.String() != tc.want {
					t.Errorf("String() = %q, want %q", got.String(), tc.want)
				}
			})
		}
	})

	t.Run("rejects", func(t *testing.T) {
		cases := map[string]string{
			"empty":                                 "",
			"blank":                                 "   ",
			"over max bytes":                        strings.Repeat("a", domain.MaxRoomNameBytes+1),
			"multibyte overflow (bytes, not runes)": strings.Repeat("字", domain.MaxRoomNameBytes/3+1),
			// Control characters: NUL would not even survive a PostgreSQL text
			// column (turning a bad request into a 500), and line breaks or ANSI
			// escapes corrupt a terminal rendering the label.
			"NUL":            "ro\x00om",
			"newline inside": "line\nbreak",
			"tab inside":     "tab\there",
			"ansi escape":    "evil\x1b[31mred",
			// Invisible formatting characters render as nothing or reorder text.
			"zero-width space": "sneaky\u200bname",
			"bidi override":    "abc\u202edef",
			"invalid utf-8":    "bad\xffbyte",
		}
		for name, raw := range cases {
			t.Run(name, func(t *testing.T) {
				if _, err := domain.NewRoomName(raw); !errors.Is(err, domain.ErrInvalidRoomName) {
					t.Errorf("NewRoomName(%q) err = %v, want ErrInvalidRoomName", raw, err)
				}
			})
		}
	})
}

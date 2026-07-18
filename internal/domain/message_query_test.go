package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestNewMessageQuery(t *testing.T) {
	t.Parallel()
	t.Run("accepts and trims", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
			want string
		}{
			{"plain", "hello", "hello"},
			{"surrounding whitespace trimmed", "  hello \t", "hello"},
			{"multiple keywords kept", "hello 世界", "hello 世界"},
			{"cjk", "雑談", "雑談"},
			// Message text is free-form, so a fragment may carry anything a
			// message may: quotes, operators-looking words, even a line break.
			{"quotes and backslash", `say "hi" \now`, `say "hi" \now`},
			{"operator-looking word", "cats OR dogs", "cats OR dogs"},
			{"newline inside", "line\nbreak", "line\nbreak"},
			{"tab inside", "col\tcol", "col\tcol"},
			// NFD input composes to NFC and CRLF canonicalizes to LF — the same
			// canonical form stored message text has, so fragments can match it.
			{"nfd composed to nfc", "café", "café"},
			{"crlf to lf", "line\r\nbreak", "line\nbreak"},
			{"max bytes", strings.Repeat("a", domain.MaxMessageQueryBytes), strings.Repeat("a", domain.MaxMessageQueryBytes)},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, err := domain.NewMessageQuery(tc.raw)
				if err != nil {
					t.Fatalf("NewMessageQuery(%q): %v", tc.raw, err)
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
			"over max bytes": strings.Repeat("a", domain.MaxMessageQueryBytes+1),
			"invalid utf-8":  "bad\xffbyte",
			// The same character policy as message text: a fragment holding a
			// rune no message can contain can never match one, and NUL would
			// not even survive the SQL boundary.
			"NUL":           "a\x00b",
			"ansi escape":   "evil\x1b[31mred",
			"bidi override": "abc\u202edef",
		}
		for name, raw := range cases {
			t.Run(name, func(t *testing.T) {
				if _, err := domain.NewMessageQuery(raw); !errors.Is(err, domain.ErrInvalidMessageQuery) {
					t.Errorf("NewMessageQuery(%q) err = %v, want ErrInvalidMessageQuery", raw, err)
				}
			})
		}
	})

	t.Run("zero value is zero", func(t *testing.T) {
		var q domain.MessageQuery
		if !q.IsZero() {
			t.Error("IsZero() = false for the zero value")
		}
		if q.String() != "" {
			t.Errorf("String() = %q for the zero value, want empty", q.String())
		}
	})
}

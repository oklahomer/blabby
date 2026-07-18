package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestNewMessageText(t *testing.T) {
	t.Parallel()
	t.Run("accepts and canonicalizes", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
			want string
		}{
			{"plain", "hello", "hello"},
			{"surrounding whitespace trimmed", "  hi \t", "hi"},
			{"newline kept", "line one\nline two", "line one\nline two"},
			{"tab kept", "col\tcol", "col\tcol"},
			{"crlf to lf", "line one\r\nline two", "line one\nline two"},
			{"lone cr to lf", "line one\rline two", "line one\nline two"},
			{"trailing crlf trimmed", "hi\r\n", "hi"},
			// NFD input (base + combining mark) composes to the NFC form:
			// か+U+3099 -> が, e+U+0301 -> é.
			{"nfd composed to nfc (kana)", "が", "が"},
			{"nfd composed to nfc (latin)", "café", "café"},
			{"nfc input unchanged", "café が", "café が"},
			// Joiners are content, not noise: ZWJ (U+200D) builds emoji
			// sequences and ZWNJ (U+200C) is orthographic in Persian text.
			{
				"emoji zwj sequence kept",
				"\U0001F468\u200d\U0001F469\u200d\U0001F467",
				"\U0001F468\u200d\U0001F469\u200d\U0001F467",
			},
			{"zero-width non-joiner kept", "می\u200cخواهم", "می\u200cخواهم"},
			{"max bytes", strings.Repeat("a", domain.MaxMessageTextBytes), strings.Repeat("a", domain.MaxMessageTextBytes)},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, err := domain.NewMessageText(tc.raw)
				if err != nil {
					t.Fatalf("NewMessageText(%q): %v", tc.raw, err)
				}
				if got.String() != tc.want {
					t.Errorf("String() = %q, want %q", got.String(), tc.want)
				}
			})
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		first, err := domain.NewMessageText("  café\r\nline two  ")
		if err != nil {
			t.Fatalf("NewMessageText: %v", err)
		}
		second, err := domain.NewMessageText(first.String())
		if err != nil {
			t.Fatalf("NewMessageText(canonical): %v", err)
		}
		if second.String() != first.String() {
			t.Errorf("re-parse changed value: %q -> %q", first.String(), second.String())
		}
	})

	t.Run("rejects blank as ErrMessageTextEmpty", func(t *testing.T) {
		cases := map[string]string{
			"empty":      "",
			"blank":      "   ",
			"only crlf":  "\r\n\r\n",
			"only mixed": " \t\n\r ",
		}
		for name, raw := range cases {
			t.Run(name, func(t *testing.T) {
				if _, err := domain.NewMessageText(raw); !errors.Is(err, domain.ErrMessageTextEmpty) {
					t.Errorf("NewMessageText(%q) err = %v, want ErrMessageTextEmpty", raw, err)
				}
			})
		}
	})

	t.Run("rejects invalid as ErrInvalidMessageText", func(t *testing.T) {
		cases := map[string]string{
			"over max bytes": strings.Repeat("a", domain.MaxMessageTextBytes+1),
			// U+0958 is composition-excluded: NFC decomposes it into
			// U+0915 U+093C (3 -> 6 bytes), so a value under the cap before
			// normalization can exceed it after; the cap applies to the
			// canonical form.
			"over max bytes after nfc": strings.Repeat("क़", domain.MaxMessageTextBytes/3),
			// Control characters other than newline and tab corrupt terminals
			// and storage; NUL would not even survive a PostgreSQL text column.
			"NUL":          "a\x00b",
			"ansi escape":  "evil\x1b[31mred",
			"vertical tab": "a\vb",
			// Explicit bidi overrides and isolates reorder surrounding text.
			"bidi override": "abc\u202edef",
			"bidi isolate":  "abc\u2066def",
			"invalid utf-8": "bad\xffbyte",
		}
		for name, raw := range cases {
			t.Run(name, func(t *testing.T) {
				if _, err := domain.NewMessageText(raw); !errors.Is(err, domain.ErrInvalidMessageText) {
					t.Errorf("NewMessageText(%q) err = %v, want ErrInvalidMessageText", raw, err)
				}
			})
		}
	})
}

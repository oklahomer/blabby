package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestNewMailAddress_NormalizesAndAccepts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "already normalized", raw: "alice@example.com", want: "alice@example.com"},
		{name: "uppercased", raw: "Alice@Example.COM", want: "alice@example.com"},
		{name: "surrounding whitespace", raw: "  bob@example.com\t", want: "bob@example.com"},
		{name: "plus addressing", raw: "carol+tag@example.com", want: "carol+tag@example.com"},
		// The full RFC 5321 atext set is legal in a dot-string local part.
		{name: "atext specials", raw: "a!b#c$%&'*+-/=?^_`{|}~@example.com", want: "a!b#c$%&'*+-/=?^_`{|}~@example.com"},
		// The grammar does not require a dotted domain.
		{name: "no dot in domain", raw: "user@localhost", want: "user@localhost"},
		{name: "hyphens inside labels", raw: "user@my-host1.example.com", want: "user@my-host1.example.com"},
		// ASCII IDNA A-labels are ordinary LDH labels and remain valid; only
		// U-label (Unicode) domains are out of scope.
		{name: "idna a-label domain", raw: "user@xn--mnchen-3ya.de", want: "user@xn--mnchen-3ya.de"},
		// RFC 5321 §4.5.3.1 boundaries: 64-byte local part, 63-byte label.
		{name: "local part at 64 bytes", raw: strings.Repeat("a", 64) + "@example.com", want: strings.Repeat("a", 64) + "@example.com"},
		{name: "label at 63 bytes", raw: "user@" + strings.Repeat("a", 63) + ".com", want: "user@" + strings.Repeat("a", 63) + ".com"},
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
	t.Parallel()
	cases := map[string]string{
		"empty":             "",
		"whitespace only":   "   ",
		"no domain":         "alice",
		"no local part":     "@example.com",
		"display-name form": "Alice <alice@example.com>",
		"two addresses":     "a@example.com, b@example.com",
		"trailing text":     "alice@example.com junk",
		// Only blabby's ASCII subset of RFC 5321 is accepted: SMTPUTF8/U-label
		// forms (RFC 6531/6532), quoted local parts, and address literals are
		// intentionally unsupported, and an ASCII-only address makes simple
		// lowercasing an exact case rule.
		"non-ascii local part": "résumé@example.com",
		"non-ascii domain":     "alice@münchen.de",
		"quoted local part":    `"john doe"@example.com`,
		"address literal":      "user@[192.168.1.1]",
		"two at signs":         "a@b@example.com",
		// Dot-string rules: atoms must be non-empty.
		"double dot in local":  "john..doe@example.com",
		"leading dot in local": ".john@example.com",
		"empty domain label":   "user@example..com",
		// RFC 5321 §4.1.2 domain labels are letters, digits, and internal
		// hyphens only — net/mail's RFC 5322 header grammar is looser and
		// would admit these.
		"underscore in domain":     "user@foo_bar.com",
		"leading hyphen in label":  "user@-example.com",
		"trailing hyphen in label": "user@example-.com",
		// RFC 5321 §4.5.3.1 size limits beyond the 254-byte total.
		"local part over 64 bytes":   strings.Repeat("a", 65) + "@example.com",
		"domain label over 63 bytes": "user@" + strings.Repeat("a", 64) + ".com",
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
	t.Parallel()
	// A local part long enough to push the whole address past the 254-byte cap.
	raw := strings.Repeat("a", domain.MaxMailAddressBytes-len("@example.com")+1) + "@example.com"
	if _, err := domain.NewMailAddress(raw); !errors.Is(err, domain.ErrInvalidMailAddress) {
		t.Errorf("NewMailAddress(len %d) err = %v, want ErrInvalidMailAddress", len(raw), err)
	}
}

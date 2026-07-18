package domain

import (
	"errors"
	"strings"
)

// MaxMailAddressBytes bounds an email address per RFC 5321 §4.5.3.1 (the
// 254-byte forward-path derivation). Login and registration both enforce it;
// the server column is unbounded TEXT, so the cap lives here.
const MaxMailAddressBytes = 254

// maxMailLocalPartBytes and maxMailDomainLabelBytes are the RFC 5321
// §4.5.3.1 size limits inside an address: 64 octets for the local part, 63
// for each domain label.
const (
	maxMailLocalPartBytes   = 64
	maxMailDomainLabelBytes = 63
)

// ErrInvalidMailAddress reports that a raw value is not a single, well-formed bare
// email address. It is the one sentinel the parser returns so callers can map any
// malformed input to a single outcome (a generic login rejection, or an
// INVALID_EMAIL registration error) without inspecting the underlying cause.
var ErrInvalidMailAddress = errors.New("mail_address: not a valid bare email address")

// MailAddress is a parsed, normalized email address used as a login identity
// and the account lookup key. It accepts an application-defined ASCII subset
// of RFC 5321: a dot-string local part of at most 64 bytes and a domain of
// letter-digit-hyphen labels, 254 bytes overall. Quoted local parts, address
// literals, and the SMTPUTF8 forms (RFC 6531/6532: Unicode local parts,
// U-label domains) are intentionally unsupported — ASCII IDNA A-labels
// (xn--…) remain valid, being ordinary LDH labels. The stored form is the
// lowercased address, so registration and login resolve the same row
// regardless of the casing or surrounding whitespace a client sends.
//
// A zero-value MailAddress is not valid; construct one with [NewMailAddress].
type MailAddress struct {
	value string
}

// NewMailAddress parses raw (after NFC normalization and trimming — a
// provable no-op for every accepted ASCII value, applied so that every text
// constructor canonicalizes uniformly) into a normalized MailAddress,
// enforcing the ASCII dot-string@LDH-domain subset described on
// [MailAddress]. Any other input is reported as [ErrInvalidMailAddress].
func NewMailAddress(raw string) (MailAddress, error) {
	trimmed := strings.TrimSpace(normalizeNFC(raw))
	if trimmed == "" || len(trimmed) > MaxMailAddressBytes {
		return MailAddress{}, ErrInvalidMailAddress
	}
	// atext excludes '@', so a valid address contains exactly one; splitting
	// at the last lets the local-part scan reject any earlier one.
	at := strings.LastIndexByte(trimmed, '@')
	if at < 0 || !isValidMailLocalPart(trimmed[:at]) || !isValidMailDomain(trimmed[at+1:]) {
		return MailAddress{}, ErrInvalidMailAddress
	}
	// Lowercasing the local part deviates from RFC 5321 §2.4, which keeps it
	// case-sensitive in principle (only the receiving host may interpret it).
	// Treating the whole address as case-insensitive is a deliberate
	// application policy — the one major providers apply — so one mailbox
	// cannot register twice as Smith@ and smith@. The failure mode at a
	// provider with case-sensitive mailboxes is contained: the verification
	// PIN is mailed to the lowercased form from the start, so a registration
	// whose casing mattered never verifies and no account exists. The ASCII
	// grammar enforced above makes strings.ToLower an exact case rule — no
	// Unicode case-folding subtlety can reach the stored form.
	return MailAddress{value: strings.ToLower(trimmed)}, nil
}

// isValidMailLocalPart reports whether s is an RFC 5321 §4.1.2 Dot-string of
// at most 64 bytes (§4.5.3.1): atext atoms joined by single dots, so no
// empty atom (leading, trailing, or doubled dots). The quoted-string
// alternative the RFC also allows is intentionally unsupported.
func isValidMailLocalPart(s string) bool {
	if s == "" || len(s) > maxMailLocalPartBytes {
		return false
	}
	for _, atom := range strings.Split(s, ".") {
		if atom == "" {
			return false
		}
		for i := 0; i < len(atom); i++ {
			if !isMailAtext(atom[i]) {
				return false
			}
		}
	}
	return true
}

// isMailAtext reports whether b is RFC 5321/5322 atext: an ASCII letter or
// digit, or one of the printable specials the grammar allows in an atom.
// Everything non-ASCII fails here, which is what confines addresses to the
// ASCII subset (multi-byte UTF-8 runes always contain bytes >= 0x80).
func isMailAtext(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-/=?^_`{|}~", b) >= 0
}

// isValidMailDomain reports whether s is a dot-joined sequence of RFC 5321
// §4.1.2 LDH labels: letters, digits, and internal hyphens, 1–63 bytes each
// (§4.5.3.1), starting and ending alphanumeric. A single label (user@localhost)
// is valid — the grammar does not require a dot. Address literals
// (user@[192.0.2.1]) are intentionally unsupported: '[' is not an LDH byte.
func isValidMailDomain(s string) bool {
	if s == "" {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > maxMailDomainLabelBytes {
			return false
		}
		for i := 0; i < len(label); i++ {
			b := label[i]
			alnum := (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
			if !alnum && (b != '-' || i == 0 || i == len(label)-1) {
				return false
			}
		}
	}
	return true
}

// String returns the normalized address — the lowercased bare form stored in the
// database. The zero value returns "", which no valid address equals.
func (m MailAddress) String() string { return m.value }

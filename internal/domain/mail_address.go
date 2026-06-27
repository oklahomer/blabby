package domain

import (
	"errors"
	"net/mail"
	"strings"
)

// MaxMailAddressBytes bounds an email address per RFC 5321. Login and
// registration both enforce it; the server column is unbounded TEXT, so the cap
// lives here.
const MaxMailAddressBytes = 254

// ErrInvalidMailAddress reports that a raw value is not a single, well-formed bare
// email address. It is the one sentinel the parser returns so callers can map any
// malformed input to a single outcome (a generic login rejection, or an
// INVALID_EMAIL registration error) without inspecting the underlying cause.
var ErrInvalidMailAddress = errors.New("mail_address: not a valid bare email address")

// MailAddress is a parsed, normalized email address used as a login identity and
// the account lookup key. Its normalized form — a lowercased bare address — is
// what service_user.mail_address stores, so registration and login resolve the
// same row regardless of the casing or surrounding whitespace a client sends.
//
// A zero-value MailAddress is not valid; construct one with [NewMailAddress].
type MailAddress struct {
	value string
}

// NewMailAddress parses raw into a normalized MailAddress. It requires a single
// bare address: net/mail.ParseAddress also accepts display-name forms like
// "Alice <a@b.com>", so the trimmed input must equal the parsed address. The
// result is lowercased. Any malformed, empty, over-length, or display-name input
// is reported as [ErrInvalidMailAddress].
func NewMailAddress(raw string) (MailAddress, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > MaxMailAddressBytes {
		return MailAddress{}, ErrInvalidMailAddress
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil {
		return MailAddress{}, ErrInvalidMailAddress
	}
	if addr.Address != trimmed {
		// A display-name form ("Alice <a@b.com>") or any wrapping was supplied; we
		// store only the bare address, so reject anything that is not already bare.
		return MailAddress{}, ErrInvalidMailAddress
	}
	return MailAddress{value: strings.ToLower(addr.Address)}, nil
}

// String returns the normalized address — the lowercased bare form stored in the
// database. The zero value returns "", which no valid address equals.
func (m MailAddress) String() string { return m.value }

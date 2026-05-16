// Package ids defines value-object types for domain identifiers that flow
// through every layer of the system.
//
// Two types live here: UserID and RoomID. They share an identical set of
// structural rules but are distinct at the Go type level so a function
// expecting one rejects the other at compile time. The package is the
// only home for identifier value objects; storage-backed entities (Room,
// User) belong to their own packages when persistence lands.
//
// The structural rules enforced by the shared parser are:
//
//   - Leading and trailing whitespace are trimmed.
//   - The result must be non-empty.
//   - The result must be no longer than [MaxIdentifierBytes] bytes.
//   - The result must not contain ASCII control characters (< 0x20 or 0x7F).
//   - The result must not contain Unicode whitespace.
//   - The result must not contain '/' (URL path delimiter).
//
// Failures are reported via the sentinel errors [ErrEmptyIdentifier],
// [ErrIdentifierTooLong], and [ErrIdentifierInvalidChar]. Constructors
// wrap the sentinel with a per-type prefix (user_id: ... / room_id: ...)
// so log readers can distinguish causes without losing the classification.
package ids

import (
	"errors"
	"strings"
	"unicode"
)

// MaxIdentifierBytes caps the length of any identifier accepted by the
// parser. The cap is a structural sanity bound — large enough to fit any
// realistic UUID, slug, or external-IdP subject, small enough to keep
// log lines and memory bounded against adversarial input.
const MaxIdentifierBytes = 256

// Sentinel errors reported by parseIdentifier and wrapped by the public
// constructors. errors.Is on a constructor result correctly identifies
// the structural cause without consulting the wrapping prefix.
var (
	ErrEmptyIdentifier       = errors.New("identifier must not be empty")
	ErrIdentifierTooLong     = errors.New("identifier exceeds maximum length")
	ErrIdentifierInvalidChar = errors.New("identifier contains invalid character")
)

// parseIdentifier applies the uniform structural rules described in the
// package documentation. The returned string is the trimmed value on
// success and the empty string on failure.
//
// The check order is deliberate: emptiness first (cheapest, most common
// failure), then length (also cheap), then per-rune scan. A caller that
// stops on the first error gets the most informative classification for
// the same operational cost.
func parseIdentifier(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrEmptyIdentifier
	}
	if len(trimmed) > MaxIdentifierBytes {
		return "", ErrIdentifierTooLong
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7F {
			return "", ErrIdentifierInvalidChar
		}
		if unicode.IsSpace(r) {
			return "", ErrIdentifierInvalidChar
		}
		if r == '/' {
			return "", ErrIdentifierInvalidChar
		}
	}
	return trimmed, nil
}

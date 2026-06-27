package domain

import (
	"errors"
	"strings"
)

const (
	// minHandleLen and maxHandleLen bound an account handle. The charset is ASCII,
	// so a handle's byte length and character count coincide.
	minHandleLen = 3
	maxHandleLen = 30
)

// ErrInvalidHandle reports that a raw value is not a 3–30 character handle of
// letters, digits, or underscore. The single sentinel lets registration map any
// malformed handle to one validation outcome.
var ErrInvalidHandle = errors.New("handle: must be 3-30 characters of letters, digits, or underscore")

// Handle is a parsed account handle: 3–30 characters of [A-Za-z0-9_], held in the
// casing the user supplied. Uniqueness is case-insensitive, so [Handle.Normalized]
// (lowercased) is what service_user.handle_norm stores while [Handle.Display] keeps
// the original casing for service_user.handle.
//
// A zero-value Handle is not valid; construct one with [NewHandle].
type Handle struct {
	display string
}

// NewHandle parses raw (after trimming) into a Handle, enforcing the length and
// charset. Mixed-case input is accepted; the normalized form is lowercased.
func NewHandle(raw string) (Handle, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < minHandleLen || len(trimmed) > maxHandleLen {
		return Handle{}, ErrInvalidHandle
	}
	for _, r := range trimmed {
		if !isHandleChar(r) {
			return Handle{}, ErrInvalidHandle
		}
	}
	return Handle{display: trimmed}, nil
}

// isHandleChar reports whether r is an allowed handle character: an ASCII letter,
// digit, or underscore.
func isHandleChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_':
		return true
	default:
		return false
	}
}

// Display returns the handle in the casing the user supplied — service_user.handle.
func (h Handle) Display() string { return h.display }

// Normalized returns the lowercased handle used for case-insensitive uniqueness —
// service_user.handle_norm.
func (h Handle) Normalized() string { return strings.ToLower(h.display) }

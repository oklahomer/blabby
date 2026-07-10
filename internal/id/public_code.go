package id

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

const (
	// publicCodeLen is the fixed symbol count of a public_code: 10 Crockford
	// symbols = 50 bits. The fixed width is load-bearing — parsing relies on it to
	// separate the single type letter from the code body (see [ParseUserCode] /
	// [ParseRoomCode]), so codes must always be exactly this long.
	publicCodeLen = 10

	userCodePrefix = 'U'
	roomCodePrefix = 'R'
)

// Sentinel errors reported by the public_code parsers.
var (
	ErrInvalidPublicCode = errors.New("public_code: must be 10 Crockford symbols")
	ErrInvalidCodePrefix = errors.New("public_code: wrong type prefix")
)

// PublicCode is the opaque, non-enumerable copy-paste code stored for a user or a
// room. It is never derived from the Snowflake (so it leaks no creation time) and
// is held bare — canonical uppercase, no type letter. The U/R type letter is
// added only at the edge via [PublicCode.FormatUser] / [PublicCode.FormatRoom].
//
// A zero-value PublicCode is not valid; construct one with [NewPublicCode] (fresh
// random) or [ParsePublicCode] (from input).
type PublicCode struct{ value string }

// NewPublicCode generates a fresh random code from crypto/rand. Each of the 10
// symbols is the low 5 bits of one random byte mapped through the Crockford
// alphabet (an unbiased mapping — see [crockfordSymbol]). Uniqueness is enforced
// downstream by the UNIQUE column plus a regenerate-on-conflict retry, not by raw
// improbability.
func NewPublicCode() (PublicCode, error) {
	buf := make([]byte, publicCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return PublicCode{}, fmt.Errorf("public_code: %w", err)
	}
	out := make([]byte, publicCodeLen)
	for i, b := range buf {
		out[i] = crockfordSymbol(b)
	}
	return PublicCode{value: string(out)}, nil
}

// ParsePublicCode normalizes raw (trim, uppercase, lenient O->0 and I/L->1) and
// validates it is exactly [publicCodeLen] Crockford symbols. It does NOT consult
// the database; resolving a code to an internal id is a separate UNIQUE-index
// lookup performed by the caller.
func ParsePublicCode(raw string) (PublicCode, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) != publicCodeLen {
		return PublicCode{}, ErrInvalidPublicCode
	}
	var b strings.Builder
	b.Grow(publicCodeLen)
	for _, r := range trimmed {
		n := normalizeCrockford(r)
		if !strings.ContainsRune(crockfordAlphabet, n) {
			return PublicCode{}, ErrInvalidPublicCode
		}
		b.WriteRune(n)
	}
	return PublicCode{value: b.String()}, nil
}

// String returns the bare canonical code (no type letter).
func (p PublicCode) String() string { return p.value }

// IsZero reports whether p is the zero value — the invalid placeholder no
// constructor emits. Callers use it to detect an unset optional code, e.g. a
// room-list query with no pagination cursor.
func (p PublicCode) IsZero() bool { return p.value == "" }

// FormatUser renders the edge form U<code>, and FormatRoom renders R<code>. There
// is no separator, so the token is one contiguous alphanumeric word for clean
// double-click selection.
func (p PublicCode) FormatUser() string { return string(userCodePrefix) + p.value }

// FormatRoom renders the edge form R<code>. See [PublicCode.FormatUser].
func (p PublicCode) FormatRoom() string { return string(roomCodePrefix) + p.value }

// ParseUserCode strips and verifies the leading U, then parses the fixed-length
// remainder as a [PublicCode]. The fixed width is what lets the first character be
// read as the type even though R is itself a Crockford symbol.
func ParseUserCode(raw string) (PublicCode, error) {
	return parsePrefixedCode(raw, userCodePrefix)
}

// ParseRoomCode strips and verifies the leading R, then parses the remainder. See
// [ParseUserCode].
func ParseRoomCode(raw string) (PublicCode, error) {
	return parsePrefixedCode(raw, roomCodePrefix)
}

func parsePrefixedCode(raw string, prefix byte) (PublicCode, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return PublicCode{}, ErrInvalidPublicCode
	}
	// The type letter is matched case-insensitively; the code body is validated by
	// ParsePublicCode.
	if c := trimmed[0]; c != prefix && c != prefix+('a'-'A') {
		return PublicCode{}, ErrInvalidCodePrefix
	}
	return ParsePublicCode(trimmed[1:])
}

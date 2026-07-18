package domain

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxRoomNameBytes caps a room's display name. Names are labels, not addresses
// (the R… public code is the identity, and names are deliberately non-unique),
// so the cap only keeps lists renderable.
const MaxRoomNameBytes = 64

// ErrInvalidRoomName reports that a raw value is not a non-blank display name of
// at most MaxRoomNameBytes bytes made of printable characters.
var ErrInvalidRoomName = errors.New("room name: must be 1-64 bytes of printable characters")

// RoomName is a parsed room display name: NFC-normalized, trimmed, non-blank,
// at most MaxRoomNameBytes UTF-8 bytes, and printable. Letters, digits, punctuation,
// symbols, emoji, and spaces (including non-ASCII spaces such as U+3000) are
// allowed — it is a label, not an identifier. Control characters (NUL would not
// even survive a PostgreSQL text column), invisible formatting characters
// (zero-width, bidi overrides), and invalid UTF-8 are rejected: they render as
// nothing or worse in a terminal and invite spoofing.
//
// A zero-value RoomName is not valid; construct one with NewRoomName.
type RoomName struct {
	value string
}

// NewRoomName parses raw (after NFC normalization and trimming) into a
// RoomName, enforcing the non-blank, byte-length, and printability rules on
// the canonical form.
func NewRoomName(raw string) (RoomName, error) {
	trimmed := strings.TrimSpace(normalizeNFC(raw))
	if trimmed == "" || len(trimmed) > MaxRoomNameBytes || !utf8.ValidString(trimmed) {
		return RoomName{}, ErrInvalidRoomName
	}
	for _, r := range trimmed {
		if !isRoomNameRune(r) {
			return RoomName{}, ErrInvalidRoomName
		}
	}
	return RoomName{value: trimmed}, nil
}

// isRoomNameRune reports whether r may appear in a room name: anything
// printable, plus whitespace runes beyond ASCII space (e.g. the ideographic
// space U+3000, which unicode.IsPrint alone excludes). Control characters are
// rejected outright — that also keeps line breaks and tabs out of the
// single-line label — and so are the Zl/Zp separators (U+2028, U+2029), which
// are line breaks in Unicode clothing that IsSpace would otherwise admit, and
// everything neither printable nor whitespace (zero-width and bidi-override
// formatting characters).
func isRoomNameRune(r rune) bool {
	if unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) {
		return false
	}
	return unicode.IsPrint(r) || unicode.IsSpace(r)
}

// String returns the display name.
func (n RoomName) String() string { return n.value }

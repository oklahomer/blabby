package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrInvalidRoomNameQuery reports that a raw value is not a usable room-name
// search fragment: non-blank, at most MaxRoomNameBytes bytes, printable.
var ErrInvalidRoomNameQuery = errors.New("room name query: must be 1-64 bytes of printable characters")

// RoomNameQuery is a parsed room-name search fragment: trimmed, non-blank, at
// most MaxRoomNameBytes bytes, and made of the same printable characters a
// [RoomName] may contain. The character rules are shared deliberately — a
// fragment holding a rune that can never appear in a display name can never
// match one, so it is rejected at the boundary instead of running a query that
// is guaranteed empty. How the fragment matches (substring, case sensitivity)
// is the repository's concern, not the value's.
//
// The zero value is valid and means "no filter"; see [RoomNameQuery.IsZero].
type RoomNameQuery struct {
	value string
}

// NewRoomNameQuery parses raw (after trimming) into a RoomNameQuery, enforcing
// the non-blank, byte-length, and printability rules.
func NewRoomNameQuery(raw string) (RoomNameQuery, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > MaxRoomNameBytes || !utf8.ValidString(trimmed) {
		return RoomNameQuery{}, ErrInvalidRoomNameQuery
	}
	for _, r := range trimmed {
		if !isRoomNameRune(r) {
			return RoomNameQuery{}, ErrInvalidRoomNameQuery
		}
	}
	return RoomNameQuery{value: trimmed}, nil
}

// IsZero reports whether the query is the zero value, i.e. no filter was
// requested. NewRoomNameQuery never returns a zero query.
func (q RoomNameQuery) IsZero() bool { return q.value == "" }

// String returns the search fragment.
func (q RoomNameQuery) String() string { return q.value }

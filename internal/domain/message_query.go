package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// MaxMessageQueryBytes caps a message search fragment. Messages themselves run
// to 4 KiB, but a search fragment far shorter than that already identifies its
// matches; the cap only bounds the work a single query can ask for.
const MaxMessageQueryBytes = 256

// ErrInvalidMessageQuery reports that a raw value is not a usable message
// search fragment: non-blank, valid UTF-8, at most MaxMessageQueryBytes bytes.
var ErrInvalidMessageQuery = errors.New("message query: must be 1-256 bytes of valid UTF-8")

// MessageQuery is a parsed message-text search fragment: trimmed, non-blank,
// valid UTF-8, at most MaxMessageQueryBytes bytes. Message text is free-form
// (unlike room names), so the fragment carries no printability rule — anything
// a message may contain, a fragment may search for. How it matches (keyword
// full-text search) is the repository's concern, not the value's.
//
// The zero value is valid and means "no filter"; see [MessageQuery.IsZero].
type MessageQuery struct {
	value string
}

// NewMessageQuery parses raw (after trimming) into a MessageQuery, enforcing
// the non-blank, byte-length, and UTF-8 rules.
func NewMessageQuery(raw string) (MessageQuery, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > MaxMessageQueryBytes || !utf8.ValidString(trimmed) {
		return MessageQuery{}, ErrInvalidMessageQuery
	}
	return MessageQuery{value: trimmed}, nil
}

// IsZero reports whether the query is the zero value, i.e. no filter was
// requested. NewMessageQuery never returns a zero query.
func (q MessageQuery) IsZero() bool { return q.value == "" }

// String returns the search fragment.
func (q MessageQuery) String() string { return q.value }

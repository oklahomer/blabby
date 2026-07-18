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
// search fragment: non-blank, valid UTF-8, at most MaxMessageQueryBytes bytes,
// carrying only characters a message may contain.
var ErrInvalidMessageQuery = errors.New("message query: must be 1-256 bytes of valid UTF-8 without control characters other than newline and tab")

// MessageQuery is a parsed message-text search fragment: NFC-normalized, LF
// newlines, trimmed, non-blank, valid UTF-8, at most MaxMessageQueryBytes
// bytes — the same canonical form [MessageText] stores, so a fragment can
// match the text it searches. It also shares [MessageText]'s character
// policy: anything a message may contain, a fragment may search for, while a
// rune no message can contain can never match one and is rejected at the
// boundary (NUL would not even survive the SQL parameter). How it matches
// (keyword full-text search) is the repository's concern, not the value's.
//
// The zero value is valid and means "no filter"; see [MessageQuery.IsZero].
type MessageQuery struct {
	value string
}

// NewMessageQuery parses raw (after newline canonicalization, NFC
// normalization, and trimming) into a MessageQuery, enforcing the non-blank,
// byte-length, and UTF-8 rules on the canonical form.
func NewMessageQuery(raw string) (MessageQuery, error) {
	trimmed := strings.TrimSpace(normalizeNFC(canonicalizeNewlines(raw)))
	if trimmed == "" || len(trimmed) > MaxMessageQueryBytes || !utf8.ValidString(trimmed) {
		return MessageQuery{}, ErrInvalidMessageQuery
	}
	for _, r := range trimmed {
		if !isMessageTextRune(r) {
			return MessageQuery{}, ErrInvalidMessageQuery
		}
	}
	return MessageQuery{value: trimmed}, nil
}

// IsZero reports whether the query is the zero value, i.e. no filter was
// requested. NewMessageQuery never returns a zero query.
func (q MessageQuery) IsZero() bool { return q.value == "" }

// String returns the search fragment.
func (q MessageQuery) String() string { return q.value }

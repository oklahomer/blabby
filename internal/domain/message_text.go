package domain

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxMessageTextBytes caps a chat message body, measured on the canonical
// form. The gateway's request-body cap is a transport concern that sits above
// this rule.
const MaxMessageTextBytes = 4 * 1024

// ErrMessageTextEmpty reports that a raw value holds no message content once
// canonicalized and trimmed. It is distinct from [ErrInvalidMessageText] so
// callers can keep reporting "text is required" separately from content that
// is present but unusable.
var ErrMessageTextEmpty = errors.New("message text: must not be blank")

// ErrInvalidMessageText reports that a raw value cannot become a message body:
// invalid UTF-8, over MaxMessageTextBytes once canonical, or carrying control
// characters other than newline and tab.
var ErrInvalidMessageText = errors.New("message text: must be at most 4096 bytes of valid UTF-8 without control characters other than newline and tab")

// MessageText is a parsed chat message body in canonical form: valid UTF-8,
// NFC-normalized, LF newlines, trimmed, non-blank, at most MaxMessageTextBytes
// bytes (see ADR-023). Message text is otherwise free-form — quotes, any
// script, emoji including their ZWJ sequences — but control characters other
// than '\n' and '\t' are rejected, as are the explicit bidi overrides and
// isolates that reorder surrounding text when rendered.
//
// A zero-value MessageText is not valid; construct one with [NewMessageText].
type MessageText struct {
	value string
}

// NewMessageText parses raw into its canonical MessageText: newlines to LF,
// Unicode to NFC, surrounding whitespace trimmed, then the emptiness,
// byte-cap, and character rules checked on that canonical form. NFC can
// lengthen a UTF-8 string, so the cap deliberately applies after
// normalization. Blank input is reported as [ErrMessageTextEmpty], everything
// else as [ErrInvalidMessageText].
func NewMessageText(raw string) (MessageText, error) {
	if !utf8.ValidString(raw) {
		return MessageText{}, ErrInvalidMessageText
	}
	text := strings.TrimSpace(normalizeNFC(canonicalizeNewlines(raw)))
	if text == "" {
		return MessageText{}, ErrMessageTextEmpty
	}
	if len(text) > MaxMessageTextBytes {
		return MessageText{}, ErrInvalidMessageText
	}
	for _, r := range text {
		if !isMessageTextRune(r) {
			return MessageText{}, ErrInvalidMessageText
		}
	}
	return MessageText{value: text}, nil
}

// isMessageTextRune reports whether r may appear in a message body. Newline
// and tab are the only control characters allowed; the rest (NUL, ANSI
// escapes) corrupt terminals and storage. Explicit bidi embeddings,
// overrides, and isolates (U+202A–U+202E, U+2066–U+2069) are rejected because
// they reorder the text around them; implicit RTL text needs none of them.
// Zero-width joiners stay allowed — they are content (emoji sequences,
// Persian orthography), which is why this is not RoomName's printability rule.
func isMessageTextRune(r rune) bool {
	if r == '\n' || r == '\t' {
		return true
	}
	if unicode.IsControl(r) {
		return false
	}
	return !isBidiControl(r)
}

// isBidiControl reports whether r is an explicit bidi embedding, override, or
// isolate control.
func isBidiControl(r rune) bool {
	return (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}

// String returns the canonical message body.
func (t MessageText) String() string { return t.value }

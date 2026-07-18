package domain

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// normalizeNFC returns s in Unicode Normalization Form C, the canonical form
// every user-supplied text value is stored and compared in (see ADR-023).
// Visually identical input then yields identical bytes regardless of whether
// the client emitted precomposed (NFC) or decomposed (NFD) sequences. Already
// normalized input — the overwhelmingly common case — is detected by a cheap
// verification pass and returned as-is.
func normalizeNFC(s string) string {
	return norm.NFC.String(s)
}

// canonicalizeNewlines maps CRLF pairs and lone CRs to LF so multi-line text
// has one stored shape regardless of the client that produced it: browsers
// submit CRLF through form encoding but LF through fetch, and other clients
// vary again.
func canonicalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

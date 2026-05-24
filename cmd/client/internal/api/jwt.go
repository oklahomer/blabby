package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

// ErrMalformedJWT is returned by DecodeSub for any token that does not
// parse as a JWT well enough to read its sub claim. The caller can
// either log and continue without a user-id or treat the WebSocket as
// uninitialised; this helper does not crash on bad input.
var ErrMalformedJWT = errors.New("malformed JWT")

// maxSubBytes caps the sub claim length. A well-formed user-id is a
// UUID (36 bytes) or a short ulid; anything larger is rejected to
// stop a malicious or buggy issuer from flooding the Profile pane.
const maxSubBytes = 128

// DecodeSub extracts the sub claim from a JWT without verifying its
// signature. The server has already verified the token at /login;
// the client decodes locally only to surface the user-id in the
// Profile pane, avoiding the need for an extra round-trip or a
// schema change to LoginResponse.
//
// Returns ErrMalformedJWT for any token that does not split into the
// canonical three segments, fails base64url decoding, fails JSON
// decoding, or has an empty sub claim.
func DecodeSub(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrMalformedJWT
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some JWT producers pad the base64; tolerate that too.
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", ErrMalformedJWT
		}
	}

	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ErrMalformedJWT
	}
	if claims.Sub == "" {
		return "", ErrMalformedJWT
	}
	if len(claims.Sub) > maxSubBytes {
		return "", ErrMalformedJWT
	}
	for _, r := range claims.Sub {
		if !unicode.IsPrint(r) {
			return "", ErrMalformedJWT
		}
	}
	return claims.Sub, nil
}

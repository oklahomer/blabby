package auth

import "errors"

// Sentinel errors returned by Authenticator implementations to let callers
// classify token validation failures without coupling to a specific token
// format (JWT, etc.). Messages are intentionally generic so that returning
// these errors directly to clients does not leak validation internals.
var (
	// ErrTokenMissing indicates that no token was supplied.
	ErrTokenMissing = errors.New("auth: token missing")

	// ErrTokenInvalid indicates that the token is malformed, has an invalid
	// signature, fails issuer/audience checks, or is otherwise unusable for
	// reasons other than expiration.
	ErrTokenInvalid = errors.New("auth: token invalid")

	// ErrTokenExpired indicates that the token's expiration time has passed.
	ErrTokenExpired = errors.New("auth: token expired")
)

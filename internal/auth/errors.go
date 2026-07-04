package auth

import "errors"

// Sentinel errors used by auth collaborators and Authenticator implementations to
// let callers classify credential and token failures without coupling to a
// specific backing store or token format. Messages are intentionally generic so
// that returning these errors directly to clients does not leak validation
// internals.
var (
	// ErrTokenMissing indicates that no token was supplied.
	ErrTokenMissing = errors.New("auth: token missing")

	// ErrTokenInvalid indicates that the token is malformed, has an invalid
	// signature, fails issuer/audience checks, or is otherwise unusable for
	// reasons other than expiration.
	ErrTokenInvalid = errors.New("auth: token invalid")

	// ErrTokenExpired indicates that the token's expiration time has passed.
	ErrTokenExpired = errors.New("auth: token expired")

	// ErrInvalidCredentials is returned by a CredentialVerifier for any login
	// rejection that must stay indistinguishable to the caller (and the client) —
	// unknown email, wrong password, or a disabled account — so the login
	// response cannot be used to enumerate accounts.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")

	// ErrAccountPending is returned by a CredentialVerifier when the password is
	// correct but the account has not completed email verification. Revealing the
	// state is safe here — the caller proved account ownership with the password —
	// and lets the client guide the user to the verification step.
	ErrAccountPending = errors.New("auth: account pending verification")

	// ErrPublicCodeUnknown is returned by a PublicCodeResolver when a public_code
	// maps to no account. ValidateToken folds it into ErrTokenInvalid — a token
	// whose subject names no live user is invalid — keeping it distinct from a
	// backend failure (see ErrIdentityUnavailable).
	ErrPublicCodeUnknown = errors.New("auth: public code unknown")

	// ErrIdentityUnavailable wraps a ValidateToken failure where the token is
	// well-formed, signed, and unexpired but its subject could not be resolved
	// because the account backend was unavailable. The token's validity is
	// indeterminate, so a caller answers 503 (retry) rather than 401 — a transient
	// outage must not make clients discard live sessions.
	ErrIdentityUnavailable = errors.New("auth: identity resolution unavailable")
)

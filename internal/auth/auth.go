// Package auth provides a pluggable authentication system with a JWT default implementation.
package auth

import (
	"context"
	"time"

	"github.com/oklahomer/blabby/internal/id"
)

// Authenticator defines the contract for authentication providers.
// Implementations must be safe for concurrent use.
//
// Latency is the implementer's responsibility. Callers may pass
// context.Background and trust the implementation to bound its own work:
// CPU-bound impls (local JWT verification) return promptly without using
// the context; IO-bound impls (JWKS, OIDC introspection, DB lookups) MUST
// enforce a deadline internally — typically via http.Client.Timeout or a
// derived context.WithTimeout — so a slow backend cannot wedge the caller.
type Authenticator interface {
	// Authenticate validates credentials and returns a token result.
	Authenticate(ctx context.Context, params AuthParams) (*Result, error)

	// ValidateToken parses and validates a token string, returning the embedded claims.
	ValidateToken(ctx context.Context, token string) (*Claims, error)
}

// AuthParams holds transport-agnostic authentication credentials. Login identity
// is the user's email; the gateway parses the request body into these fields.
type AuthParams struct {
	MailAddress string
	Password    string
}

// VerifiedUser is the identity a successful credential check yields: the internal
// UserID and the opaque public_code that becomes the JWT subject. Both are typed
// value objects, so a verifier cannot hand back an unparsed identifier.
type VerifiedUser struct {
	UserID     id.UserID
	PublicCode id.PublicCode
}

// CredentialVerifier checks an email/password pair against the account store and
// returns the verified identity. It is the login-path collaborator the
// JWTAuthenticator depends on. Implementations return ErrInvalidCredentials for
// any rejection (unknown email, wrong password, non-active account) so the caller
// cannot distinguish the cases, and a wrapped error for an infrastructure failure.
//
// Latency is the implementer's responsibility (see Authenticator): a DB-backed
// verifier bounds its own work.
type CredentialVerifier interface {
	VerifyCredentials(ctx context.Context, mailAddress, password string) (VerifiedUser, error)
}

// PublicCodeResolver maps a user's opaque public_code (carried as the JWT subject)
// to their internal UserID. It is the token-validation collaborator: ValidateToken
// parses the U… subject and resolves it here, so the numeric id never rides on the
// wire. The mapping is immutable, so an implementation may cache it.
//
// It returns ErrPublicCodeUnknown when the code maps to no account; any other
// error is taken as a backend failure (which ValidateToken surfaces as
// ErrIdentityUnavailable). Like the verifier, an IO-bound implementation bounds
// its own work.
type PublicCodeResolver interface {
	ResolveUserID(ctx context.Context, code id.PublicCode) (id.UserID, error)
}

// Result holds the outcome of a successful authentication. UserID is the
// authenticated user's internal identifier; the token's JWT Subject carries the
// user's opaque U… public_code, not this id, so the numeric id never rides on the
// wire.
type Result struct {
	UserID id.UserID
	Token  string
}

// Claims holds the standard RFC 7519 claims extracted from a validated token.
// UserID is the internal identifier the U… Subject resolves to (via a
// PublicCodeResolver) — it carries the typed identifier so downstream consumers
// cannot bypass the structural rules. A Subject that is not a well-formed U… code,
// or that resolves to no account, causes ValidateToken to fail with
// ErrTokenInvalid before a Claims value is constructed.
type Claims struct {
	UserID    id.UserID
	Issuer    string
	Audience  []string
	ExpiresAt time.Time
	IssuedAt  time.Time
}

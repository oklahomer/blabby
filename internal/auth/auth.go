// Package auth provides a pluggable authentication system with a JWT default implementation.
package auth

import (
	"context"
	"time"
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

// AuthParams holds transport-agnostic authentication credentials.
type AuthParams struct {
	Username string
	Password string
}

// Result holds the outcome of a successful authentication.
type Result struct {
	UserID string
	Token  string
}

// Claims holds the standard RFC 7519 claims extracted from a validated token.
type Claims struct {
	Subject   string    `json:"sub"`
	Issuer    string    `json:"iss"`
	Audience  []string  `json:"aud"`
	ExpiresAt time.Time `json:"exp"`
	IssuedAt  time.Time `json:"iat"`
}

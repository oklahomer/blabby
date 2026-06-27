package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/oklahomer/blabby/internal/id"
)

const (
	defaultExpiration = 10 * time.Minute

	// Issuer identifies the blabby system as the token issuer.
	Issuer = "blabby"

	// Audience identifies the blabby system as the intended token recipient.
	Audience = "blabby"
)

// Option configures a JWTAuthenticator.
type Option func(*JWTAuthenticator)

// WithExpiration sets the token expiration duration.
// It panics if d is zero or negative.
func WithExpiration(d time.Duration) Option {
	if d <= 0 {
		panic("auth: expiration must be positive")
	}
	return func(a *JWTAuthenticator) {
		a.expiration = d
	}
}

// JWTAuthenticator implements Authenticator using JWT tokens. It delegates
// credential checking to a CredentialVerifier and token-subject resolution to a
// PublicCodeResolver, so it owns the token format while the account store owns
// identity.
type JWTAuthenticator struct {
	signingKey []byte
	expiration time.Duration
	verifier   CredentialVerifier
	resolver   PublicCodeResolver
}

// NewJWTAuthenticator creates a new JWT-based authenticator.
// It panics if signingKey is empty or either collaborator is nil.
func NewJWTAuthenticator(signingKey []byte, verifier CredentialVerifier, resolver PublicCodeResolver, opts ...Option) *JWTAuthenticator {
	if len(signingKey) == 0 {
		panic("auth: signing key must not be empty")
	}
	if verifier == nil {
		panic("auth: credential verifier must not be nil")
	}
	if resolver == nil {
		panic("auth: public-code resolver must not be nil")
	}

	keyCopy := make([]byte, len(signingKey))
	copy(keyCopy, signingKey)

	a := &JWTAuthenticator{
		signingKey: keyCopy,
		expiration: defaultExpiration,
		verifier:   verifier,
		resolver:   resolver,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Authenticate verifies the email/password and returns a signed JWT whose subject
// is the user's opaque public_code (U…), never the internal numeric id. It
// preserves the verifier's error classification for the caller: a credential
// rejection wraps ErrInvalidCredentials (the gateway answers a generic 401), while
// an infrastructure failure is returned with its detail intact (answered with a
// 500). Scrubbing detail from the client response is the gateway handler's job.
func (a *JWTAuthenticator) Authenticate(ctx context.Context, params AuthParams) (*Result, error) {
	user, err := a.verifier.VerifyCredentials(ctx, params.MailAddress, params.Password)
	if err != nil {
		// Preserve the verifier's classification: ErrInvalidCredentials stays
		// matchable so the caller answers a generic 401, while an infrastructure
		// failure keeps its detail for the caller to log and answer with a 500.
		return nil, fmt.Errorf("authenticate: %w", err)
	}

	now := time.Now()
	claims := &jwt.RegisteredClaims{
		Subject:   user.PublicCode.FormatUser(),
		Issuer:    Issuer,
		Audience:  jwt.ClaimStrings{Audience},
		ExpiresAt: jwt.NewNumericDate(now.Add(a.expiration)),
		IssuedAt:  jwt.NewNumericDate(now),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(a.signingKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign token: %w", err)
	}

	slog.Info("authentication successful", "user_id", user.UserID.String())

	return &Result{
		UserID: user.UserID,
		Token:  tokenString,
	}, nil
}

// ValidateToken parses a JWT string and returns the embedded claims.
//
// On failure the returned error wraps ErrTokenExpired, ErrTokenInvalid, or
// ErrIdentityUnavailable so callers can classify the failure via errors.Is
// without importing the underlying JWT library. The underlying jwt error is
// preserved in the chain so callers asserting on it continue to work.
//
// A Subject that is not a well-formed U… public_code, or that resolves to no
// account, is treated as an invalid token rather than a separate failure mode —
// the JWT carried bytes that cannot identify a live user, which is what
// ErrTokenInvalid means at this boundary. (A token minted with the pre-migration
// numeric subject therefore fails here and the holder must re-login.) If the
// subject is well-formed but cannot be resolved because the account backend is
// unavailable, ValidateToken returns ErrIdentityUnavailable instead, so a
// transient outage does not invalidate an otherwise-valid session.
func (a *JWTAuthenticator) ValidateToken(ctx context.Context, tokenString string) (*Claims, error) {
	if tokenString == "" {
		return nil, fmt.Errorf("%w: empty token", ErrTokenInvalid)
	}

	claims := new(jwt.RegisteredClaims)
	_, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return a.signingKey, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuer(Issuer), jwt.WithAudience(Audience))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("%w: %w", ErrTokenExpired, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrTokenInvalid, err)
	}

	code, err := id.ParseUserCode(claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTokenInvalid, err)
	}
	userID, err := a.resolver.ResolveUserID(ctx, code)
	if errors.Is(err, ErrPublicCodeUnknown) {
		// The subject names no live account: the token cannot identify anyone.
		return nil, fmt.Errorf("%w: %w", ErrTokenInvalid, err)
	}
	if err != nil {
		// The backend was unavailable, so validity is indeterminate; the caller
		// answers 503 rather than discarding a possibly-valid session as invalid.
		return nil, fmt.Errorf("%w: %w", ErrIdentityUnavailable, err)
	}

	// jwt.WithExpirationRequired() guarantees ExpiresAt is non-nil — the
	// parser returns an error caught above otherwise. IssuedAt is RFC 7519
	// optional and the library does not require it; dereferencing
	// claims.IssuedAt without a nil check would panic on any token that
	// omits the iat claim.
	var issuedAt time.Time
	if claims.IssuedAt != nil {
		issuedAt = claims.IssuedAt.Time
	}
	return &Claims{
		UserID:    userID,
		Issuer:    claims.Issuer,
		Audience:  claims.Audience,
		ExpiresAt: claims.ExpiresAt.Time,
		IssuedAt:  issuedAt,
	}, nil
}

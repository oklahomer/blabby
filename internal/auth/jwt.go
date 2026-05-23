package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

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

// JWTAuthenticator implements Authenticator using JWT tokens.
type JWTAuthenticator struct {
	signingKey []byte
	expiration time.Duration
	store      UserStore
}

// NewJWTAuthenticator creates a new JWT-based authenticator.
// It panics if signingKey is empty or store is nil.
func NewJWTAuthenticator(signingKey []byte, store UserStore, opts ...Option) *JWTAuthenticator {
	if len(signingKey) == 0 {
		panic("auth: signing key must not be empty")
	}
	if store == nil {
		panic("auth: store must not be nil")
	}

	keyCopy := make([]byte, len(signingKey))
	copy(keyCopy, signingKey)

	a := &JWTAuthenticator{
		signingKey: keyCopy,
		expiration: defaultExpiration,
		store:      store,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Authenticate validates credentials and returns a signed JWT. The
// store's user ID is parsed into an id.UserID before signing — a
// structurally invalid stored ID is a server-side data-integrity issue
// and surfaces to the client as a generic credential failure, with the
// underlying cause logged for operators.
func (a *JWTAuthenticator) Authenticate(_ context.Context, params AuthParams) (*Result, error) {
	user, err := a.store.Lookup(params.Username)
	if err != nil {
		slog.Warn("authentication failed", "username", params.Username, "reason", "user_not_found")
		return nil, errors.New("failed to authenticate: invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(params.Password)); err != nil {
		slog.Warn("authentication failed", "username", params.Username, "reason", "invalid_credentials")
		return nil, errors.New("failed to authenticate: invalid credentials")
	}

	userID, err := id.NewUserID(user.ID)
	if err != nil {
		slog.Error("authentication failed", "username", params.Username, "reason", "store_user_id_invalid", "error", err)
		return nil, errors.New("failed to authenticate: invalid credentials")
	}

	now := time.Now()
	claims := &jwt.RegisteredClaims{
		Subject:   userID.String(),
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

	slog.Info("authentication successful", "user_id", userID.String())

	return &Result{
		UserID: userID,
		Token:  tokenString,
	}, nil
}

// ValidateToken parses a JWT string and returns the embedded claims.
//
// On failure the returned error always wraps one of ErrTokenExpired or
// ErrTokenInvalid so callers can classify the failure via errors.Is without
// importing the underlying JWT library. The underlying jwt error is preserved
// in the chain so callers asserting on it continue to work.
//
// A structurally invalid Subject claim (empty, control characters, etc.)
// is treated as an invalid token rather than a separate failure mode —
// the JWT carried bytes that cannot identify a user, which is what
// ErrTokenInvalid means at this boundary.
func (a *JWTAuthenticator) ValidateToken(_ context.Context, tokenString string) (*Claims, error) {
	if tokenString == "" {
		return nil, fmt.Errorf("%w: empty token", ErrTokenInvalid)
	}

	token, err := jwt.ParseWithClaims(tokenString, &jwt.RegisteredClaims{}, func(token *jwt.Token) (interface{}, error) {
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

	claims, ok := token.Claims.(*jwt.RegisteredClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("%w: invalid claims", ErrTokenInvalid)
	}

	userID, err := id.NewUserID(claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTokenInvalid, err)
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

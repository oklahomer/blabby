package auth_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/oklahomer/blabby/internal/auth"
)

// stubUserStore is a single-user [auth.UserStore] for exercising
// Authenticate paths that the in-memory store cannot reach — chiefly a
// stored user whose ID is structurally invalid.
type stubUserStore struct {
	user auth.StoredUser
}

func (s stubUserStore) Lookup(username string) (*auth.StoredUser, error) {
	if username != s.user.Username {
		return nil, fmt.Errorf("user not found: %s", username)
	}
	u := s.user
	return &u, nil
}

func TestWithExpiration_PanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("WithExpiration(%v) did not panic", d)
				}
			}()
			auth.WithExpiration(d)
		})
	}
}

func TestNewJWTAuthenticator_Panics(t *testing.T) {
	store := auth.NewInMemoryUserStore()
	tests := []struct {
		name       string
		signingKey []byte
		store      auth.UserStore
	}{
		{name: "empty signing key", signingKey: nil, store: store},
		{name: "nil store", signingKey: []byte("secret"), store: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewJWTAuthenticator did not panic")
				}
			}()
			auth.NewJWTAuthenticator(tt.signingKey, tt.store)
		})
	}
}

// TestJWTAuthenticator_Authenticate_InvalidStoredUserID covers the
// data-integrity path: credentials match, but the stored user ID does not
// satisfy id.ParseUserID. The client must see a generic credential failure, not
// a distinct error, and no token is issued.
func TestJWTAuthenticator_Authenticate_InvalidStoredUserID(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("setup: hash password: %v", err)
	}
	store := stubUserStore{user: auth.StoredUser{
		ID:           "bad/id", // non-numeric, rejected by id.ParseUserID
		Username:     "mallory",
		PasswordHash: hash,
	}}
	authenticator := auth.NewJWTAuthenticator([]byte("secret"), store)

	result, err := authenticator.Authenticate(context.Background(), auth.AuthParams{
		Username: "mallory",
		Password: "pw",
	})
	if err == nil {
		t.Fatalf("expected error for invalid stored user ID, got result %+v", result)
	}
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if got, want := err.Error(), "failed to authenticate: invalid credentials"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	for _, leaked := range []string{"bad/id", "identifier"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("error %q leaked store-integrity detail %q", err, leaked)
		}
	}
}

// TestJWTAuthenticator_ConcurrentUse drives Authenticate and ValidateToken
// from many goroutines against one authenticator. The Authenticator contract
// promises concurrency safety; this is the regression guard that fails under
// -race if a future dependency or key-rotation change introduces shared
// mutable state.
func TestJWTAuthenticator_ConcurrentUse(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("setup: hash password: %v", err)
	}
	store := stubUserStore{user: auth.StoredUser{
		ID:           auth.UserIDBob.String(),
		Username:     "bob",
		PasswordHash: hash,
	}}
	authenticator := auth.NewJWTAuthenticator([]byte("test-secret"), store)
	ctx := context.Background()

	seed, err := authenticator.Authenticate(ctx, auth.AuthParams{Username: "bob", Password: "pw"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	const (
		goroutines = 8
		iterations = 4
	)
	start := make(chan struct{})
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(worker int) {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				if _, err := authenticator.Authenticate(ctx, auth.AuthParams{Username: "bob", Password: "pw"}); err != nil {
					errs <- fmt.Errorf("worker %d concurrent Authenticate: %w", worker, err)
					return
				}
				if _, err := authenticator.ValidateToken(ctx, seed.Token); err != nil {
					errs <- fmt.Errorf("worker %d concurrent ValidateToken: %w", worker, err)
					return
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestJWTAuthenticator_Authenticate(t *testing.T) {
	store := auth.NewInMemoryUserStore()
	authenticator := auth.NewJWTAuthenticator([]byte("test-secret"), store)
	ctx := context.Background()

	tests := []struct {
		name     string
		params   auth.AuthParams
		wantErr  bool
		wantUser string
	}{
		{
			name:     "valid credentials produce signed JWT",
			params:   auth.AuthParams{Username: "alice", Password: "alice123"},
			wantErr:  false,
			wantUser: auth.UserIDAlice.String(),
		},
		{
			name:    "invalid password is rejected",
			params:  auth.AuthParams{Username: "alice", Password: "wrong"},
			wantErr: true,
		},
		{
			name:    "unknown user is rejected",
			params:  auth.AuthParams{Username: "unknown", Password: "pass"},
			wantErr: true,
		},
		{
			name:    "empty username is rejected",
			params:  auth.AuthParams{Username: "", Password: "pass"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := authenticator.Authenticate(ctx, tt.params)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.UserID.String() != tt.wantUser {
				t.Errorf("UserID = %q, want %q", result.UserID.String(), tt.wantUser)
			}
			if result.Token == "" {
				t.Error("expected non-empty token")
			}
		})
	}
}

func TestJWTAuthenticator_ValidateToken(t *testing.T) {
	secret := []byte("test-secret")
	store := auth.NewInMemoryUserStore()
	authenticator := auth.NewJWTAuthenticator(secret, store)
	ctx := context.Background()

	// Generate a valid token first.
	result, err := authenticator.Authenticate(ctx, auth.AuthParams{
		Username: "bob",
		Password: "bob123",
	})
	if err != nil {
		t.Fatalf("setup: failed to authenticate: %v", err)
	}

	t.Run("valid token returns correct claims", func(t *testing.T) {
		claims, err := authenticator.ValidateToken(ctx, result.Token)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims.UserID.String() != auth.UserIDBob.String() {
			t.Errorf("UserID = %q, want %q", claims.UserID.String(), auth.UserIDBob.String())
		}
		if claims.Issuer != auth.Issuer {
			t.Errorf("Issuer = %q, want %q", claims.Issuer, auth.Issuer)
		}
		if len(claims.Audience) != 1 || claims.Audience[0] != auth.Audience {
			t.Errorf("Audience = %v, want [%q]", claims.Audience, auth.Audience)
		}
		if claims.ExpiresAt.IsZero() {
			t.Error("ExpiresAt should not be zero")
		}
		if claims.IssuedAt.IsZero() {
			t.Error("IssuedAt should not be zero")
		}
	})

	t.Run("malformed token is rejected with ErrTokenInvalid", func(t *testing.T) {
		_, err := authenticator.ValidateToken(ctx, "not-a-jwt")
		if err == nil {
			t.Fatal("expected error for malformed token")
		}
		if !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("expected ErrTokenInvalid, got %v", err)
		}
	})

	t.Run("empty token is rejected with ErrTokenInvalid", func(t *testing.T) {
		_, err := authenticator.ValidateToken(ctx, "")
		if err == nil {
			t.Fatal("expected error for empty token")
		}
		if !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("expected ErrTokenInvalid, got %v", err)
		}
	})

	t.Run("token with wrong signing key is rejected with ErrTokenInvalid", func(t *testing.T) {
		otherAuth := auth.NewJWTAuthenticator([]byte("other-secret"), store)
		otherResult, err := otherAuth.Authenticate(ctx, auth.AuthParams{
			Username: "alice",
			Password: "alice123",
		})
		if err != nil {
			t.Fatalf("setup: %v", err)
		}

		_, err = authenticator.ValidateToken(ctx, otherResult.Token)
		if err == nil {
			t.Fatal("expected error for token signed with different key")
		}
		if !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("expected ErrTokenInvalid, got %v", err)
		}
	})

	// Subjects that pass JWT-library validation but fail the id.ParseUserID
	// rules must surface as invalid tokens — a JWT that cannot identify a
	// user is invalid at this boundary, not a separate failure mode.
	subjectCases := []struct {
		name    string
		subject string
	}{
		{name: "empty subject", subject: ""},
		{name: "non-numeric subject", subject: "alice"},
		{name: "non-positive subject", subject: "0"},
		{name: "subject with NUL", subject: "1\x00"},
	}
	for _, tc := range subjectCases {
		t.Run("structurally invalid subject ("+tc.name+") is rejected with ErrTokenInvalid", func(t *testing.T) {
			tokenString := signTokenWithSubject(t, secret, tc.subject, time.Now().Add(time.Hour))
			_, err := authenticator.ValidateToken(ctx, tokenString)
			if err == nil {
				t.Fatal("expected error for token with invalid subject")
			}
			if !errors.Is(err, auth.ErrTokenInvalid) {
				t.Errorf("expected ErrTokenInvalid, got %v", err)
			}
		})
	}
}

// signTokenWithSubject forges a JWT with an arbitrary Subject claim so
// the ValidateToken parser sees a structurally invalid subject after
// otherwise-successful signature verification. The Issuer and Audience
// match the authenticator's expected values.
func signTokenWithSubject(t *testing.T, secret []byte, subject string, expiresAt time.Time) string {
	t.Helper()
	claims := &jwt.RegisteredClaims{
		Subject:   subject,
		Issuer:    auth.Issuer,
		Audience:  jwt.ClaimStrings{auth.Audience},
		ExpiresAt: jwt.NewNumericDate(expiresAt),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatalf("failed to sign forged token: %v", err)
	}
	return signed
}

// TestJWTAuthenticator_ValidateToken_MissingIssuedAt covers a JWT that
// omits the optional iat claim. RFC 7519 does not require iat, and the
// JWT library does not enforce it; without a nil-check on
// claims.IssuedAt, ValidateToken would panic on a perfectly legal token.
func TestJWTAuthenticator_ValidateToken_MissingIssuedAt(t *testing.T) {
	secret := []byte("test-secret")
	store := auth.NewInMemoryUserStore()
	authenticator := auth.NewJWTAuthenticator(secret, store)

	claims := &jwt.RegisteredClaims{
		Subject:   auth.UserIDAlice.String(),
		Issuer:    auth.Issuer,
		Audience:  jwt.ClaimStrings{auth.Audience},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		// IssuedAt intentionally omitted.
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatalf("failed to sign token without iat: %v", err)
	}

	got, err := authenticator.ValidateToken(context.Background(), signed)
	if err != nil {
		t.Fatalf("ValidateToken returned error for token without iat: %v", err)
	}
	if !got.IssuedAt.IsZero() {
		t.Errorf("IssuedAt: got %v, want zero time", got.IssuedAt)
	}
	if got.UserID.String() != auth.UserIDAlice.String() {
		t.Errorf("UserID: got %q, want %q", got.UserID.String(), auth.UserIDAlice.String())
	}
}

// TestJWTAuthenticator_ValidateToken_AlgConfusion exercises the signing-method
// guard: a token presented with the "none" algorithm (the classic JWT
// alg-confusion attack) must be rejected as invalid, never accepted as
// unsigned. This locks the defense in place against a future refactor.
func TestJWTAuthenticator_ValidateToken_AlgConfusion(t *testing.T) {
	store := auth.NewInMemoryUserStore()
	authenticator := auth.NewJWTAuthenticator([]byte("test-secret"), store)

	claims := &jwt.RegisteredClaims{
		Subject:   auth.UserIDAlice.String(),
		Issuer:    auth.Issuer,
		Audience:  jwt.ClaimStrings{auth.Audience},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("setup: sign none-alg token: %v", err)
	}

	if _, err := authenticator.ValidateToken(context.Background(), signed); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid for none-alg token, got %v", err)
	}
}

func TestJWTAuthenticator_ExpiredToken(t *testing.T) {
	store := auth.NewInMemoryUserStore()
	authenticator := auth.NewJWTAuthenticator(
		[]byte("test-secret"),
		store,
		auth.WithExpiration(1*time.Second),
	)
	ctx := context.Background()

	result, err := authenticator.Authenticate(ctx, auth.AuthParams{
		Username: "charlie",
		Password: "charlie123",
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Wait for token to expire. JWT exp has second granularity.
	time.Sleep(2 * time.Second)

	_, err = authenticator.ValidateToken(ctx, result.Token)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if !errors.Is(err, auth.ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
	// The original jwt error should remain reachable via errors.Is so existing
	// callers / tests that assert on the underlying chain keep working.
	if !errors.Is(err, jwt.ErrTokenExpired) {
		t.Errorf("expected underlying jwt.ErrTokenExpired in chain, got %v", err)
	}
}

func TestJWTAuthenticator_ConfigurableExpiration(t *testing.T) {
	store := auth.NewInMemoryUserStore()
	authenticator := auth.NewJWTAuthenticator(
		[]byte("test-secret"),
		store,
		auth.WithExpiration(2*time.Hour),
	)
	ctx := context.Background()

	result, err := authenticator.Authenticate(ctx, auth.AuthParams{
		Username: "alice",
		Password: "alice123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	claims, err := authenticator.ValidateToken(ctx, result.Token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.UserID.String() != auth.UserIDAlice.String() {
		t.Errorf("UserID = %q, want %q", claims.UserID.String(), auth.UserIDAlice.String())
	}
}

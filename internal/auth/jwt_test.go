package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/oklahomer/blabby/internal/auth"
)

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

	// Subjects that pass JWT-library validation but fail the ids.NewUserID
	// rules must surface as invalid tokens — a JWT that cannot identify a
	// user is invalid at this boundary, not a separate failure mode.
	subjectCases := []struct {
		name    string
		subject string
	}{
		{name: "empty subject", subject: ""},
		{name: "whitespace-only subject", subject: " \t"},
		{name: "subject with NUL", subject: "alice\x00"},
		{name: "subject with slash", subject: "foo/bar"},
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

package auth_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/id"
)

// Test fixtures: a user the fake verifier accepts by email/password and whose
// public_code the fake resolver maps back to the matching UserID.
const (
	testEmail    = "alice@example.com"
	testPassword = "alice123"
	testCodeBody = "A000000042" // 10 Crockford symbols
)

func testUserID(t *testing.T) id.UserID {
	t.Helper()
	uid, err := id.NewUserID(42)
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	return uid
}

func testPublicCode(t *testing.T) id.PublicCode {
	t.Helper()
	code, err := id.ParsePublicCode(testCodeBody)
	if err != nil {
		t.Fatalf("ParsePublicCode: %v", err)
	}
	return code
}

// fakeVerifier accepts exactly one email/password pair, returning the configured
// VerifiedUser; everything else is ErrInvalidCredentials. A non-nil err overrides
// the result to simulate an infrastructure failure.
type fakeVerifier struct {
	email    string
	password string
	user     auth.VerifiedUser
	err      error
}

func (f fakeVerifier) VerifyCredentials(_ context.Context, mail, password string) (auth.VerifiedUser, error) {
	if f.err != nil {
		return auth.VerifiedUser{}, f.err
	}
	if mail != f.email || password != f.password {
		return auth.VerifiedUser{}, auth.ErrInvalidCredentials
	}
	return f.user, nil
}

// fakeResolver maps known public codes to UserIDs; an unknown code reports
// auth.ErrPublicCodeUnknown, and a non-nil err overrides to simulate a backend
// failure.
type fakeResolver struct {
	byCode map[string]id.UserID
	err    error
}

func (f fakeResolver) ResolveUserID(_ context.Context, code id.PublicCode) (id.UserID, error) {
	if f.err != nil {
		return id.UserID{}, f.err
	}
	uid, ok := f.byCode[code.String()]
	if !ok {
		return id.UserID{}, auth.ErrPublicCodeUnknown
	}
	return uid, nil
}

// newTestAuthenticator builds an authenticator whose verifier accepts
// testEmail/testPassword and whose resolver maps testPublicCode back to
// testUserID, returning both fixtures for assertions.
func newTestAuthenticator(t *testing.T, secret []byte, opts ...auth.Option) (*auth.JWTAuthenticator, id.UserID, id.PublicCode) {
	t.Helper()
	uid := testUserID(t)
	code := testPublicCode(t)
	verifier := fakeVerifier{
		email:    testEmail,
		password: testPassword,
		user:     auth.VerifiedUser{UserID: uid, PublicCode: code},
	}
	resolver := fakeResolver{byCode: map[string]id.UserID{code.String(): uid}}
	return auth.NewJWTAuthenticator(secret, verifier, resolver, opts...), uid, code
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
	verifier := fakeVerifier{}
	resolver := fakeResolver{}
	tests := []struct {
		name       string
		signingKey []byte
		verifier   auth.CredentialVerifier
		resolver   auth.PublicCodeResolver
	}{
		{name: "empty signing key", signingKey: nil, verifier: verifier, resolver: resolver},
		{name: "nil verifier", signingKey: []byte("secret"), verifier: nil, resolver: resolver},
		{name: "nil resolver", signingKey: []byte("secret"), verifier: verifier, resolver: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewJWTAuthenticator did not panic")
				}
			}()
			auth.NewJWTAuthenticator(tt.signingKey, tt.verifier, tt.resolver)
		})
	}
}

// TestJWTAuthenticator_Authenticate_VerifierError covers the failure path's
// classification: Authenticate preserves the verifier's error so the caller can
// answer a credential rejection with a generic 401 but an infrastructure failure
// with a 500. (Scrubbing internal detail from the client response is the gateway
// handler's job, not the authenticator's.) No token is issued either way.
func TestJWTAuthenticator_Authenticate_VerifierError(t *testing.T) {
	t.Run("credential rejection stays matchable as ErrInvalidCredentials", func(t *testing.T) {
		// fakeVerifier{} accepts no credentials, so it rejects with ErrInvalidCredentials.
		authenticator := auth.NewJWTAuthenticator([]byte("secret"), fakeVerifier{}, fakeResolver{})

		result, err := authenticator.Authenticate(context.Background(), auth.AuthParams{
			MailAddress: "mallory@example.com", Password: "pw",
		})
		if result != nil {
			t.Errorf("expected nil result, got %+v", result)
		}
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("error = %v, want it to wrap ErrInvalidCredentials", err)
		}
	})

	t.Run("infrastructure failure is distinguishable and preserves detail", func(t *testing.T) {
		infra := errors.New("dial tcp: connection refused")
		authenticator := auth.NewJWTAuthenticator([]byte("secret"), fakeVerifier{err: infra}, fakeResolver{})

		result, err := authenticator.Authenticate(context.Background(), auth.AuthParams{
			MailAddress: "mallory@example.com", Password: "pw",
		})
		if result != nil {
			t.Errorf("expected nil result, got %+v", result)
		}
		if errors.Is(err, auth.ErrInvalidCredentials) {
			t.Error("an infrastructure failure must not be classified as invalid credentials")
		}
		if !errors.Is(err, infra) {
			t.Errorf("error = %v, want it to preserve the underlying failure", err)
		}
	})
}

// TestJWTAuthenticator_Authenticate_SubjectIsPublicCode pins the headline change:
// the issued token's subject is the user's U… public_code, not the numeric id.
func TestJWTAuthenticator_Authenticate_SubjectIsPublicCode(t *testing.T) {
	secret := []byte("test-secret")
	authenticator, uid, code := newTestAuthenticator(t, secret)

	result, err := authenticator.Authenticate(context.Background(), auth.AuthParams{
		MailAddress: testEmail, Password: testPassword,
	})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	var claims jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(result.Token, &claims); err != nil {
		t.Fatalf("parse token: %v", err)
	}
	if claims.Subject != code.FormatUser() {
		t.Errorf("subject = %q, want the U… code %q", claims.Subject, code.FormatUser())
	}
	if claims.Subject == uid.String() {
		t.Errorf("subject must not be the numeric id %q", uid.String())
	}
}

// TestJWTAuthenticator_ConcurrentUse drives Authenticate and ValidateToken from
// many goroutines against one authenticator. The Authenticator contract promises
// concurrency safety; this fails under -race if a future change introduces shared
// mutable state.
func TestJWTAuthenticator_ConcurrentUse(t *testing.T) {
	authenticator, _, _ := newTestAuthenticator(t, []byte("test-secret"))
	ctx := context.Background()

	seed, err := authenticator.Authenticate(ctx, auth.AuthParams{MailAddress: testEmail, Password: testPassword})
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
				if _, err := authenticator.Authenticate(ctx, auth.AuthParams{MailAddress: testEmail, Password: testPassword}); err != nil {
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
	authenticator, uid, _ := newTestAuthenticator(t, []byte("test-secret"))
	ctx := context.Background()

	tests := []struct {
		name    string
		params  auth.AuthParams
		wantErr bool
	}{
		{
			name:   "valid credentials produce signed JWT",
			params: auth.AuthParams{MailAddress: testEmail, Password: testPassword},
		},
		{
			name:    "invalid password is rejected",
			params:  auth.AuthParams{MailAddress: testEmail, Password: "wrong"},
			wantErr: true,
		},
		{
			name:    "unknown email is rejected",
			params:  auth.AuthParams{MailAddress: "unknown@example.com", Password: testPassword},
			wantErr: true,
		},
		{
			name:    "empty email is rejected",
			params:  auth.AuthParams{MailAddress: "", Password: testPassword},
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
			if result.UserID != uid {
				t.Errorf("UserID = %q, want %q", result.UserID, uid)
			}
			if result.Token == "" {
				t.Error("expected non-empty token")
			}
		})
	}
}

func TestJWTAuthenticator_ValidateToken(t *testing.T) {
	secret := []byte("test-secret")
	authenticator, uid, code := newTestAuthenticator(t, secret)
	ctx := context.Background()

	result, err := authenticator.Authenticate(ctx, auth.AuthParams{MailAddress: testEmail, Password: testPassword})
	if err != nil {
		t.Fatalf("setup: failed to authenticate: %v", err)
	}

	t.Run("valid token resolves to the user's id", func(t *testing.T) {
		claims, err := authenticator.ValidateToken(ctx, result.Token)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims.UserID != uid {
			t.Errorf("UserID = %q, want %q", claims.UserID, uid)
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
		if !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("expected ErrTokenInvalid, got %v", err)
		}
	})

	t.Run("empty token is rejected with ErrTokenInvalid", func(t *testing.T) {
		_, err := authenticator.ValidateToken(ctx, "")
		if !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("expected ErrTokenInvalid, got %v", err)
		}
	})

	t.Run("token with wrong signing key is rejected with ErrTokenInvalid", func(t *testing.T) {
		otherAuth, _, _ := newTestAuthenticator(t, []byte("other-secret"))
		otherResult, err := otherAuth.Authenticate(ctx, auth.AuthParams{MailAddress: testEmail, Password: testPassword})
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		if _, err := authenticator.ValidateToken(ctx, otherResult.Token); !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("expected ErrTokenInvalid, got %v", err)
		}
	})

	// A valid U… subject that resolves to no account is an invalid token: the
	// JWT names a user that does not exist (e.g. deleted since issue).
	t.Run("unresolvable subject is rejected with ErrTokenInvalid", func(t *testing.T) {
		unknown, err := id.ParsePublicCode("B000000099")
		if err != nil {
			t.Fatalf("ParsePublicCode: %v", err)
		}
		token := signTokenWithSubject(t, secret, unknown.FormatUser(), time.Now().Add(time.Hour))
		if _, err := authenticator.ValidateToken(ctx, token); !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("expected ErrTokenInvalid, got %v", err)
		}
	})

	// Subjects that pass JWT-library validation but are not well-formed U… codes
	// (including a pre-migration numeric subject) must surface as invalid tokens.
	subjectCases := []struct {
		name    string
		subject string
	}{
		{name: "empty subject", subject: ""},
		{name: "numeric subject (pre-migration)", subject: uid.String()},
		{name: "missing U prefix", subject: code.String()},
		{name: "wrong-length code body", subject: "Ualice"},
		{name: "subject with NUL", subject: "U\x00"},
	}
	for _, tc := range subjectCases {
		t.Run("invalid subject ("+tc.name+") is rejected with ErrTokenInvalid", func(t *testing.T) {
			token := signTokenWithSubject(t, secret, tc.subject, time.Now().Add(time.Hour))
			if _, err := authenticator.ValidateToken(ctx, token); !errors.Is(err, auth.ErrTokenInvalid) {
				t.Errorf("expected ErrTokenInvalid, got %v", err)
			}
		})
	}
}

// TestJWTAuthenticator_ValidateToken_ResolverError covers a transient resolution
// failure (e.g. the account store is unreachable): the token cannot be confirmed,
// so it surfaces as ErrIdentityUnavailable (answered 503) rather than being
// invalidated as a bad token (401) — a live session survives a backend blip.
func TestJWTAuthenticator_ValidateToken_ResolverError(t *testing.T) {
	secret := []byte("test-secret")
	uid := testUserID(t)
	code := testPublicCode(t)
	verifier := fakeVerifier{email: testEmail, password: testPassword, user: auth.VerifiedUser{UserID: uid, PublicCode: code}}
	resolver := fakeResolver{err: errors.New("database unreachable")}
	authenticator := auth.NewJWTAuthenticator(secret, verifier, resolver)

	token := signTokenWithSubject(t, secret, code.FormatUser(), time.Now().Add(time.Hour))
	_, err := authenticator.ValidateToken(context.Background(), token)
	if !errors.Is(err, auth.ErrIdentityUnavailable) {
		t.Errorf("expected ErrIdentityUnavailable, got %v", err)
	}
	if errors.Is(err, auth.ErrTokenInvalid) {
		t.Error("a backend failure must not be classified as an invalid token")
	}
}

// signTokenWithSubject forges a JWT with an arbitrary Subject claim so the
// ValidateToken parser sees a chosen subject after otherwise-successful signature
// verification. The Issuer and Audience match the authenticator's expected values.
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

// TestJWTAuthenticator_ValidateToken_MissingIssuedAt covers a JWT that omits the
// optional iat claim. RFC 7519 does not require iat and the JWT library does not
// enforce it; without a nil-check on claims.IssuedAt, ValidateToken would panic on
// a perfectly legal token.
func TestJWTAuthenticator_ValidateToken_MissingIssuedAt(t *testing.T) {
	secret := []byte("test-secret")
	authenticator, uid, code := newTestAuthenticator(t, secret)

	claims := &jwt.RegisteredClaims{
		Subject:   code.FormatUser(),
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
	if got.UserID != uid {
		t.Errorf("UserID: got %q, want %q", got.UserID, uid)
	}
}

// TestJWTAuthenticator_ValidateToken_AlgConfusion exercises the signing-method
// guard: a token presented with the "none" algorithm (the classic alg-confusion
// attack) must be rejected as invalid, never accepted as unsigned.
func TestJWTAuthenticator_ValidateToken_AlgConfusion(t *testing.T) {
	authenticator, _, code := newTestAuthenticator(t, []byte("test-secret"))

	claims := &jwt.RegisteredClaims{
		Subject:   code.FormatUser(),
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
	authenticator, _, _ := newTestAuthenticator(t, []byte("test-secret"), auth.WithExpiration(1*time.Second))
	ctx := context.Background()

	result, err := authenticator.Authenticate(ctx, auth.AuthParams{MailAddress: testEmail, Password: testPassword})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Wait for token to expire. JWT exp has second granularity.
	time.Sleep(2 * time.Second)

	_, err = authenticator.ValidateToken(ctx, result.Token)
	if !errors.Is(err, auth.ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
	// The original jwt error should remain reachable via errors.Is so callers that
	// assert on the underlying chain keep working.
	if !errors.Is(err, jwt.ErrTokenExpired) {
		t.Errorf("expected underlying jwt.ErrTokenExpired in chain, got %v", err)
	}
}

func TestJWTAuthenticator_ConfigurableExpiration(t *testing.T) {
	authenticator, uid, _ := newTestAuthenticator(t, []byte("test-secret"), auth.WithExpiration(2*time.Hour))
	ctx := context.Background()

	result, err := authenticator.Authenticate(ctx, auth.AuthParams{MailAddress: testEmail, Password: testPassword})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	claims, err := authenticator.ValidateToken(ctx, result.Token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.UserID != uid {
		t.Errorf("UserID = %q, want %q", claims.UserID, uid)
	}
}

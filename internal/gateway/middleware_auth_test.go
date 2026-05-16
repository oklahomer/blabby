package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/ids"
)

// mustUserID is a test helper that panics on construction failure. Used
// to build fake Claims with valid UserIDs without inflating every table
// row with error-handling boilerplate.
func mustUserID(t *testing.T, raw string) ids.UserID {
	t.Helper()
	uid, err := ids.NewUserID(raw)
	if err != nil {
		t.Fatalf("mustUserID(%q): %v", raw, err)
	}
	return uid
}

func TestAuthMiddleware(t *testing.T) {
	const validToken = "valid-token"
	const validUserID = "user-123"

	tests := []struct {
		name             string
		authHeader       string
		setHeader        bool
		validateTokenFn  func(ctx context.Context, token string) (*auth.Claims, error)
		wantStatus       int
		wantErrorCode    int // 0 means downstream invoked
		wantDownstream   bool
		wantContextUser  string // empty unless wantDownstream is true and we want to verify
		wantContextCheck bool
	}{
		{
			name:           "missing Authorization header returns 401 with code 1003",
			setHeader:      false,
			wantStatus:     http.StatusUnauthorized,
			wantErrorCode:  int(CodeAuthMissingToken),
			wantDownstream: false,
		},
		{
			name:           "header without Bearer prefix returns 401 with code 1003 (Basic scheme)",
			setHeader:      true,
			authHeader:     "Basic dXNlcjpwYXNz",
			wantStatus:     http.StatusUnauthorized,
			wantErrorCode:  int(CodeAuthMissingToken),
			wantDownstream: false,
		},
		{
			name:           "header without Bearer prefix returns 401 with code 1003 (Token scheme)",
			setHeader:      true,
			authHeader:     "Token abc",
			wantStatus:     http.StatusUnauthorized,
			wantErrorCode:  int(CodeAuthMissingToken),
			wantDownstream: false,
		},
		{
			name:           "raw token without scheme returns 401 with code 1003",
			setHeader:      true,
			authHeader:     "abcdef",
			wantStatus:     http.StatusUnauthorized,
			wantErrorCode:  int(CodeAuthMissingToken),
			wantDownstream: false,
		},
		{
			name:           "Bearer prefix with empty token returns 401 with code 1003",
			setHeader:      true,
			authHeader:     "Bearer ",
			wantStatus:     http.StatusUnauthorized,
			wantErrorCode:  int(CodeAuthMissingToken),
			wantDownstream: false,
		},
		{
			name:           "lowercase bearer is rejected with code 1003",
			setHeader:      true,
			authHeader:     "bearer abc",
			wantStatus:     http.StatusUnauthorized,
			wantErrorCode:  int(CodeAuthMissingToken),
			wantDownstream: false,
		},
		{
			name:       "valid token invokes downstream and injects user ID",
			setHeader:  true,
			authHeader: "Bearer " + validToken,
			validateTokenFn: func(ctx context.Context, token string) (*auth.Claims, error) {
				if token != validToken {
					return nil, fmt.Errorf("unexpected token passed to authenticator: %q", token)
				}
				return &auth.Claims{UserID: mustUserID(t, validUserID)}, nil
			},
			wantStatus:       http.StatusOK,
			wantDownstream:   true,
			wantContextUser:  validUserID,
			wantContextCheck: true,
		},
		{
			name:       "expired token returns 401 with code 1002",
			setHeader:  true,
			authHeader: "Bearer expired",
			validateTokenFn: func(ctx context.Context, token string) (*auth.Claims, error) {
				return nil, fmt.Errorf("%w: underlying detail", auth.ErrTokenExpired)
			},
			wantStatus:     http.StatusUnauthorized,
			wantErrorCode:  int(CodeAuthExpiredToken),
			wantDownstream: false,
		},
		{
			name:       "invalid token sentinel returns 401 with code 1001",
			setHeader:  true,
			authHeader: "Bearer bogus",
			validateTokenFn: func(ctx context.Context, token string) (*auth.Claims, error) {
				return nil, fmt.Errorf("%w: bad sig", auth.ErrTokenInvalid)
			},
			wantStatus:     http.StatusUnauthorized,
			wantErrorCode:  int(CodeAuthInvalidToken),
			wantDownstream: false,
		},
		{
			name:       "generic non-sentinel error falls back to code 1001",
			setHeader:  true,
			authHeader: "Bearer bogus",
			validateTokenFn: func(ctx context.Context, token string) (*auth.Claims, error) {
				return nil, errors.New("something exotic happened")
			},
			wantStatus:     http.StatusUnauthorized,
			wantErrorCode:  int(CodeAuthInvalidToken),
			wantDownstream: false,
		},
		{
			name:       "nil claims with nil error returns 401 with code 1001",
			setHeader:  true,
			authHeader: "Bearer ok",
			validateTokenFn: func(ctx context.Context, token string) (*auth.Claims, error) {
				return nil, nil
			},
			wantStatus:     http.StatusUnauthorized,
			wantErrorCode:  int(CodeAuthInvalidToken),
			wantDownstream: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGateway(&stubAuthenticator{validateTokenFn: tt.validateTokenFn}, nil, nil)

			downstreamInvoked := false
			var capturedUserID string
			var capturedUserOK bool
			downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downstreamInvoked = true
				capturedUserID, capturedUserOK = auth.UserIDFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if tt.setHeader {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()

			g.authMiddleware(downstream).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			if downstreamInvoked != tt.wantDownstream {
				t.Errorf("downstream invocation: got %v, want %v", downstreamInvoked, tt.wantDownstream)
			}

			if tt.wantContextCheck {
				if !capturedUserOK {
					t.Errorf("expected UserIDFromContext ok=true, got false")
				}
				if capturedUserID != tt.wantContextUser {
					t.Errorf("user ID in context: got %q, want %q", capturedUserID, tt.wantContextUser)
				}
			}

			if tt.wantErrorCode != 0 {
				if got := rec.Header().Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type: got %q, want application/json", got)
				}
				resp := decodeErrorResponse(t, rec.Body)
				if resp.Error.Code != tt.wantErrorCode {
					t.Errorf("error.code: got %d, want %d", resp.Error.Code, tt.wantErrorCode)
				}
				if resp.Error.Status == "" || resp.Error.Status == "UNKNOWN_ERROR" {
					t.Errorf("error.status missing or unknown: %q", resp.Error.Status)
				}
				if resp.Error.Message == "" {
					t.Errorf("error.message must not be empty")
				}
			}
		})
	}
}

func TestAuthMiddleware_DoesNotLeakTokenToLogs(t *testing.T) {
	const secretToken = "SECRET-TOKEN-XYZ"

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	g := NewGateway(&stubAuthenticator{
		validateTokenFn: func(ctx context.Context, token string) (*auth.Claims, error) {
			return nil, fmt.Errorf("%w: detail", auth.ErrTokenInvalid)
		},
	}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+secretToken)
	rec := httptest.NewRecorder()

	g.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream must not be invoked on auth failure")
	})).ServeHTTP(rec, req)

	if bytes.Contains(buf.Bytes(), []byte(secretToken)) {
		t.Errorf("token leaked into log output: %s", buf.String())
	}
	if bytes.Contains(buf.Bytes(), []byte("Bearer "+secretToken)) {
		t.Errorf("Authorization header value leaked into log output: %s", buf.String())
	}
}

func TestGateway_RequireAuth_WrapsHandlerFunc(t *testing.T) {
	g := NewGateway(&stubAuthenticator{
		validateTokenFn: func(ctx context.Context, token string) (*auth.Claims, error) {
			return &auth.Claims{UserID: mustUserID(t, "alice")}, nil
		},
	}, nil, nil)

	handler := g.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserIDFromContext(r.Context())
		if !ok || uid != "alice" {
			t.Errorf("expected alice in context, got %q ok=%v", uid, ok)
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("Authorization", "Bearer x")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestGateway_RequireAuth_RejectsMissingHeader(t *testing.T) {
	g := NewGateway(&stubAuthenticator{}, nil, nil)
	handler := g.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run when auth fails")
	})

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", rec.Code)
	}
	resp := decodeErrorResponse(t, rec.Body)
	if resp.Error.Code != int(CodeAuthMissingToken) {
		t.Errorf("code: got %d, want %d", resp.Error.Code, CodeAuthMissingToken)
	}
}

// Quick sanity check that token strings containing whitespace are rejected
// rather than passed verbatim to the authenticator. RFC 6750 disallows
// in-token whitespace; trimming would mask malformed input.
func TestAuthMiddleware_RejectsTokenWithWhitespace(t *testing.T) {
	g := NewGateway(&stubAuthenticator{
		validateTokenFn: func(ctx context.Context, token string) (*auth.Claims, error) {
			t.Fatalf("authenticator must not be called for whitespace token, got %q", token)
			return nil, nil
		},
	}, nil, nil)

	for _, header := range []string{
		"Bearer abc def",
		"Bearer abc\t",
		// Header has two spaces between scheme and token. The parser strips
		// only the 7-byte "Bearer " prefix, so the surviving token is " abc"
		// (leading space) — caught by the in-token whitespace check.
		"Bearer  abc",
	} {
		t.Run(strings.ReplaceAll(header, " ", "_"), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", header)
			rec := httptest.NewRecorder()

			g.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("downstream must not be invoked")
			})).ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status: got %d, want 401 (body=%s)", rec.Code, rec.Body.String())
			}
			resp := decodeErrorResponse(t, rec.Body)
			if resp.Error.Code != int(CodeAuthMissingToken) {
				t.Errorf("code: got %d, want %d", resp.Error.Code, CodeAuthMissingToken)
			}
		})
	}
}

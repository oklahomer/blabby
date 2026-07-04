package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/errcode"
)

func decodeErrorResponse(t *testing.T, body io.Reader) ErrorResponse {
	t.Helper()
	var resp ErrorResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	return resp
}

func TestHandleLogin(t *testing.T) {
	successAuth := func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
		if params.MailAddress != "alice@example.com" || params.Password != "secret" {
			return nil, auth.ErrInvalidCredentials
		}
		return &auth.Result{UserID: mustUserID(t, "1"), Token: "signed.jwt.token"}, nil
	}

	tests := []struct {
		name          string
		body          string
		authFn        func(ctx context.Context, params auth.AuthParams) (*auth.Result, error)
		wantStatus    int
		wantErrorCode errcode.Code // 0 if no error expected
	}{
		{
			name:       "valid credentials returns 200 with token",
			body:       `{"mail_address":"alice@example.com","password":"secret"}`,
			authFn:     successAuth,
			wantStatus: http.StatusOK,
		},
		{
			name:          "invalid credentials returns 401 with code 1001",
			body:          `{"mail_address":"alice@example.com","password":"wrong"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusUnauthorized,
			wantErrorCode: errcode.AuthInvalidToken,
		},
		{
			// The verifier proved the password before classifying the account as
			// pending, so revealing the state is not an enumeration oracle.
			name: "pending account with correct password returns 401 with code 1004",
			body: `{"mail_address":"alice@example.com","password":"secret"}`,
			authFn: func(context.Context, auth.AuthParams) (*auth.Result, error) {
				return nil, fmt.Errorf("authenticate: %w", auth.ErrAccountPending)
			},
			wantStatus:    http.StatusUnauthorized,
			wantErrorCode: errcode.AuthAccountPending,
		},
		{
			name:          "empty body returns 400 with code 4001",
			body:          ``,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "malformed JSON returns 400 with code 4001",
			body:          `{"mail_address":"alice@example.com"`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "missing mail address returns 400 with code 4001",
			body:          `{"password":"secret"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "missing password returns 400 with code 4001",
			body:          `{"mail_address":"alice@example.com"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "empty mail address and password returns 400",
			body:          `{"mail_address":"","password":""}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "whitespace-only mail address returns 400",
			body:          `{"mail_address":"   ","password":"secret"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "whitespace-only password returns 400",
			body:          `{"mail_address":"alice@example.com","password":"\t\n"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "trailing JSON object returns 400",
			body:          `{"mail_address":"alice@example.com","password":"secret"}{"x":1}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "trailing junk after JSON returns 400",
			body:          `{"mail_address":"alice@example.com","password":"secret"} garbage`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "mail address over length cap returns 400",
			body:          `{"mail_address":"` + strings.Repeat("a", domain.MaxMailAddressBytes+1) + `","password":"secret"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "password over length cap returns 400",
			body:          `{"mail_address":"alice@example.com","password":"` + strings.Repeat("p", maxPasswordBytes+1) + `"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gatewayWithAuth(&stubAuthenticator{authenticateFn: tt.authFn})

			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			g.RegisterRoutes().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type: got %q, want application/json", got)
			}

			if tt.wantErrorCode != 0 {
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
				return
			}

			var loginResp LoginResponse
			if err := json.NewDecoder(rec.Body).Decode(&loginResp); err != nil {
				t.Fatalf("failed to decode success response: %v", err)
			}
			if loginResp.Token == "" {
				t.Errorf("expected non-empty token in success response")
			}
		})
	}
}

func TestHandleLogin_AuthErrorMessageDoesNotLeakDetails(t *testing.T) {
	g := gatewayWithAuth(&stubAuthenticator{
		authenticateFn: func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
			// A credential rejection that wraps a leaky internal message: the client
			// must still see only the generic response.
			return nil, fmt.Errorf("user alice not found in database table users: %w", auth.ErrInvalidCredentials)
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"mail_address":"alice@example.com","password":"x"}`))
	rec := httptest.NewRecorder()
	g.RegisterRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", rec.Code)
	}
	resp := decodeErrorResponse(t, rec.Body)
	if strings.Contains(resp.Error.Message, "database") || strings.Contains(resp.Error.Message, "alice") {
		t.Errorf("error message leaks internal details: %q", resp.Error.Message)
	}
	if resp.Error.Code != errcode.AuthInvalidToken {
		t.Errorf("code: got %d, want %d", resp.Error.Code, errcode.AuthInvalidToken)
	}
}

// TestHandleLogin_InfrastructureErrorReturns500 covers a non-credential failure
// (e.g. the account store is unreachable): it must surface as a 500, not a
// misleading 401, and must not leak the infrastructure detail to the client.
func TestHandleLogin_InfrastructureErrorReturns500(t *testing.T) {
	g := gatewayWithAuth(&stubAuthenticator{
		authenticateFn: func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
			return nil, errors.New("dial tcp 127.0.0.1:5432: connection refused")
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"mail_address":"alice@example.com","password":"secret"}`))
	rec := httptest.NewRecorder()
	g.RegisterRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
	resp := decodeErrorResponse(t, rec.Body)
	if resp.Error.Code != errcode.InternalError {
		t.Errorf("code: got %d, want %d", resp.Error.Code, errcode.InternalError)
	}
	if strings.Contains(resp.Error.Message, "connection refused") || strings.Contains(resp.Error.Message, "5432") {
		t.Errorf("error message leaks infrastructure detail: %q", resp.Error.Message)
	}
}

func TestHandleLogin_NilResultFromAuthenticatorReturns500(t *testing.T) {
	g := gatewayWithAuth(&stubAuthenticator{
		authenticateFn: func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
			return nil, nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"mail_address":"alice@example.com","password":"secret"}`))
	rec := httptest.NewRecorder()
	g.RegisterRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", rec.Code)
	}
	resp := decodeErrorResponse(t, rec.Body)
	if resp.Error.Code != errcode.InternalError {
		t.Errorf("code: got %d, want %d", resp.Error.Code, errcode.InternalError)
	}
}

func TestHandleLogin_EmptyTokenFromAuthenticatorReturns500(t *testing.T) {
	g := gatewayWithAuth(&stubAuthenticator{
		authenticateFn: func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
			return &auth.Result{UserID: mustUserID(t, "1"), Token: ""}, nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"mail_address":"alice@example.com","password":"secret"}`))
	rec := httptest.NewRecorder()
	g.RegisterRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
	resp := decodeErrorResponse(t, rec.Body)
	if resp.Error.Code != errcode.InternalError {
		t.Errorf("code: got %d, want %d", resp.Error.Code, errcode.InternalError)
	}
}

func TestHandleLogin_BodyTooLargeReturns400(t *testing.T) {
	g := gatewayWithAuth(&stubAuthenticator{
		authenticateFn: func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
			return &auth.Result{Token: "tok"}, nil
		},
	})

	// Build a JSON body larger than the 1 MB cap.
	huge := strings.Repeat("a", (1<<20)+10)
	body := `{"mail_address":"alice@example.com","password":"` + huge + `"}`

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	g.RegisterRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
	resp := decodeErrorResponse(t, rec.Body)
	if resp.Error.Code != errcode.InvalidRequest {
		t.Errorf("code: got %d, want %d", resp.Error.Code, errcode.InvalidRequest)
	}
}

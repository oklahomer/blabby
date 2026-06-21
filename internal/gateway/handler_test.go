package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/auth"
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
		if params.Username != "alice" || params.Password != "secret" {
			return nil, errors.New("invalid credentials")
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
			body:       `{"username":"alice","password":"secret"}`,
			authFn:     successAuth,
			wantStatus: http.StatusOK,
		},
		{
			name:          "invalid credentials returns 401 with code 1001",
			body:          `{"username":"alice","password":"wrong"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusUnauthorized,
			wantErrorCode: errcode.AuthInvalidToken,
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
			body:          `{"username":"alice"`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "missing username returns 400 with code 4001",
			body:          `{"password":"secret"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "missing password returns 400 with code 4001",
			body:          `{"username":"alice"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "empty username and password returns 400",
			body:          `{"username":"","password":""}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "whitespace-only username returns 400",
			body:          `{"username":"   ","password":"secret"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "whitespace-only password returns 400",
			body:          `{"username":"alice","password":"\t\n"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "trailing JSON object returns 400",
			body:          `{"username":"alice","password":"secret"}{"x":1}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "trailing junk after JSON returns 400",
			body:          `{"username":"alice","password":"secret"} garbage`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "username over length cap returns 400",
			body:          `{"username":"` + strings.Repeat("a", maxUsernameBytes+1) + `","password":"secret"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "password over length cap returns 400",
			body:          `{"username":"alice","password":"` + strings.Repeat("p", maxPasswordBytes+1) + `"}`,
			authFn:        successAuth,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGateway(&stubAuthenticator{authenticateFn: tt.authFn}, nil, nil, nil)

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
	g := NewGateway(&stubAuthenticator{
		authenticateFn: func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
			return nil, errors.New("user alice not found in database table users")
		},
	}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice","password":"x"}`))
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

func TestHandleLogin_NilResultFromAuthenticatorReturns500(t *testing.T) {
	g := NewGateway(&stubAuthenticator{
		authenticateFn: func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
			return nil, nil
		},
	}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice","password":"secret"}`))
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
	g := NewGateway(&stubAuthenticator{
		authenticateFn: func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
			return &auth.Result{UserID: mustUserID(t, "1"), Token: ""}, nil
		},
	}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice","password":"secret"}`))
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
	g := NewGateway(&stubAuthenticator{
		authenticateFn: func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
			return &auth.Result{Token: "tok"}, nil
		},
	}, nil, nil, nil)

	// Build a JSON body larger than the 1 MB cap.
	huge := strings.Repeat("a", (1<<20)+10)
	body := `{"username":"alice","password":"` + huge + `"}`

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

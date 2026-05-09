package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/auth"
)

type stubAuthenticator struct {
	authenticateFn  func(ctx context.Context, params auth.AuthParams) (*auth.Result, error)
	validateTokenFn func(ctx context.Context, token string) (*auth.Claims, error)
}

func (s *stubAuthenticator) Authenticate(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
	return s.authenticateFn(ctx, params)
}

func (s *stubAuthenticator) ValidateToken(ctx context.Context, token string) (*auth.Claims, error) {
	if s.validateTokenFn == nil {
		return nil, errors.New("validateTokenFn not configured")
	}
	return s.validateTokenFn(ctx, token)
}

func TestNewGateway(t *testing.T) {
	stub := &stubAuthenticator{}
	g := NewGateway(stub, nil, nil)
	if g == nil {
		t.Fatal("NewGateway returned nil")
	}
	if g.auth != stub {
		t.Fatal("Gateway.auth does not reference the authenticator passed to the constructor")
	}
}

func TestRegisterRoutes_LoginRouteRegistered(t *testing.T) {
	g := NewGateway(&stubAuthenticator{
		authenticateFn: func(ctx context.Context, params auth.AuthParams) (*auth.Result, error) {
			return &auth.Result{UserID: "u1", Token: "tok"}, nil
		},
	}, nil, nil)
	handler := g.RegisterRoutes()

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"a","password":"b"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for POST /login, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestRegisterRoutes_WrongMethodReturns405WithJSONEnvelope(t *testing.T) {
	g := NewGateway(&stubAuthenticator{}, nil, nil)
	handler := g.RegisterRoutes()

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /login, got %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow header: got %q, want POST", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", got)
	}
	resp := decodeErrorResponse(t, rec.Body)
	if resp.Error.Code != int(CodeInvalidRequest) {
		t.Errorf("error.code: got %d, want %d", resp.Error.Code, CodeInvalidRequest)
	}
}

func TestRegisterRoutes_UnknownPathReturns404WithJSONEnvelope(t *testing.T) {
	g := NewGateway(&stubAuthenticator{}, nil, nil)
	handler := g.RegisterRoutes()

	req := httptest.NewRequest(http.MethodPost, "/unknown", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown path, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", got)
	}
	resp := decodeErrorResponse(t, rec.Body)
	if resp.Error.Code != int(CodeInvalidRequest) {
		t.Errorf("error.code: got %d, want %d", resp.Error.Code, CodeInvalidRequest)
	}
}

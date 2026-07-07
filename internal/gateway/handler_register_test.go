package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/persistence"
)

// fakeRegistrar is a stub Registrar: it records the params it received and returns
// a configured result/error.
type fakeRegistrar struct {
	result    RegisterResult
	err       error
	called    bool
	gotParams RegisterParams
}

func (f *fakeRegistrar) Register(_ context.Context, params RegisterParams) (RegisterResult, error) {
	f.called = true
	f.gotParams = params
	return f.result, f.err
}

func gatewayWithRegistrar(r Registrar) *Gateway {
	return NewGateway(Deps{Registration: r})
}

func TestHandleRegister(t *testing.T) {
	const validBody = `{"mail_address":"alice@example.com","handle":"Alice","password":"supersecret12"}`

	tests := []struct {
		name          string
		body          string
		registrar     *fakeRegistrar
		wantStatus    int
		wantErrorCode errcode.Code // 0 means a 201 success
		wantReached   bool         // whether the service should have been called
	}{
		{
			name:        "valid registration returns 201",
			body:        validBody,
			registrar:   &fakeRegistrar{result: RegisterResult{PublicCode: "UA000000042"}},
			wantStatus:  http.StatusCreated,
			wantReached: true,
		},
		{name: "empty body", body: ``, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "malformed JSON", body: `{"mail_address":`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "trailing JSON", body: validBody + `{"x":1}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "missing mail_address", body: `{"handle":"alice","password":"supersecret12"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "missing handle", body: `{"mail_address":"a@example.com","password":"supersecret12"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "missing password", body: `{"mail_address":"a@example.com","handle":"alice"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "invalid email", body: `{"mail_address":"not-an-email","handle":"alice","password":"supersecret12"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidEmail},
		{name: "handle too short", body: `{"mail_address":"a@example.com","handle":"ab","password":"supersecret12"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "handle bad char", body: `{"mail_address":"a@example.com","handle":"al ice","password":"supersecret12"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "weak password", body: `{"mail_address":"a@example.com","handle":"alice","password":"short"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.WeakPassword},
		{
			name:          "password too long",
			body:          `{"mail_address":"a@example.com","handle":"alice","password":"` + strings.Repeat("p", maxPasswordBytes+1) + `"}`,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.InvalidRequest,
		},
		{
			name:          "duplicate email returns 409",
			body:          validBody,
			registrar:     &fakeRegistrar{err: persistence.ErrMailAddressTaken},
			wantStatus:    http.StatusConflict,
			wantErrorCode: errcode.EmailAlreadyRegistered,
			wantReached:   true,
		},
		{
			name:          "duplicate handle returns 409",
			body:          validBody,
			registrar:     &fakeRegistrar{err: persistence.ErrHandleTaken},
			wantStatus:    http.StatusConflict,
			wantErrorCode: errcode.HandleAlreadyTaken,
			wantReached:   true,
		},
		{
			name:          "resend rate limited returns 429",
			body:          validBody,
			registrar:     &fakeRegistrar{err: persistence.ErrVerificationRateLimited},
			wantStatus:    http.StatusTooManyRequests,
			wantErrorCode: errcode.VerificationRateLimited,
			wantReached:   true,
		},
		{
			name:          "service failure returns 500",
			body:          validBody,
			registrar:     &fakeRegistrar{err: errors.New("dial tcp: connection refused")},
			wantStatus:    http.StatusInternalServerError,
			wantErrorCode: errcode.InternalError,
			wantReached:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := tc.registrar
			if reg == nil {
				reg = &fakeRegistrar{}
			}
			g := gatewayWithRegistrar(reg)

			req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			g.RegisterRoutes().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if reg.called != tc.wantReached {
				t.Errorf("service reached = %v, want %v (validation must short-circuit)", reg.called, tc.wantReached)
			}

			if tc.wantErrorCode != 0 {
				resp := decodeErrorResponse(t, rec.Body)
				if resp.Error.Code != tc.wantErrorCode {
					t.Errorf("error.code: got %d, want %d", resp.Error.Code, tc.wantErrorCode)
				}
				if resp.Error.Message == "" {
					t.Error("error.message must not be empty")
				}
				return
			}

			var resp RegisterResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode success response: %v", err)
			}
			if resp.PublicCode != "UA000000042" {
				t.Errorf("public_code = %q, want UA000000042", resp.PublicCode)
			}
		})
	}
}

func TestHandleRegister_ParsesAndNormalizes(t *testing.T) {
	reg := &fakeRegistrar{result: RegisterResult{PublicCode: "UA000000042"}}
	g := gatewayWithRegistrar(reg)

	body := `{"mail_address":"  Alice@Example.COM ","handle":"Alice_99","password":"supersecret12"}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	rec := httptest.NewRecorder()
	g.RegisterRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	// The handler parses into domain values before calling the service: the email is
	// normalized and the handle keeps its display casing.
	if got := reg.gotParams.MailAddress.String(); got != "alice@example.com" {
		t.Errorf("service got mail_address %q, want alice@example.com", got)
	}
	if got := reg.gotParams.Handle.Display(); got != "Alice_99" {
		t.Errorf("service got handle %q, want Alice_99", got)
	}
}

func TestHandleRegister_WrongMethodReturns405(t *testing.T) {
	g := gatewayWithRegistrar(&fakeRegistrar{})
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	rec := httptest.NewRecorder()
	g.RegisterRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow header: got %q, want POST", got)
	}
}

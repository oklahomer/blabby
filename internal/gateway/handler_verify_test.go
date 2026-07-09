package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/persistence"
)

// fakeVerificationService is a stub VerificationService recording its calls and
// returning configured errors.
type fakeVerificationService struct {
	verifyErr    error
	resendErr    error
	verifyCalled bool
	resendCalled bool
	gotVerify    VerifyParams
	gotResend    ResendParams
}

func (f *fakeVerificationService) Verify(_ context.Context, p VerifyParams) error {
	f.verifyCalled = true
	f.gotVerify = p
	return f.verifyErr
}

func (f *fakeVerificationService) Resend(_ context.Context, p ResendParams) error {
	f.resendCalled = true
	f.gotResend = p
	return f.resendErr
}

func gatewayWithVerification(v VerificationService) *Gateway {
	return NewGateway(Deps{Verification: v})
}

func TestHandleVerify(t *testing.T) {
	const validBody = `{"mail_address":"alice@example.com","pin":"123456"}`

	tests := []struct {
		name          string
		body          string
		svc           *fakeVerificationService
		wantStatus    int
		wantErrorCode errcode.Code // 0 means a 200 success
		wantReached   bool
	}{
		{name: "valid verification returns 200", body: validBody, svc: &fakeVerificationService{}, wantStatus: http.StatusOK, wantReached: true},
		{name: "empty body", body: ``, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "malformed JSON", body: `{"pin":`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "trailing JSON", body: validBody + `{}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "missing pin", body: `{"mail_address":"alice@example.com"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "missing mail_address", body: `{"pin":"123456"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "pin too long", body: `{"mail_address":"alice@example.com","pin":"` + strings.Repeat("9", maxPINBytes+1) + `"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "invalid email", body: `{"mail_address":"nope","pin":"123456"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidEmail},
		{
			name:          "wrong/expired PIN returns 400 VERIFICATION_INVALID",
			body:          validBody,
			svc:           &fakeVerificationService{verifyErr: errVerifyInvalid},
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.VerificationInvalid,
			wantReached:   true,
		},
		{
			name:          "locked returns uniform 400 VERIFICATION_INVALID",
			body:          validBody,
			svc:           &fakeVerificationService{verifyErr: errVerifyInvalid},
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: errcode.VerificationInvalid,
			wantReached:   true,
		},
		{
			name:          "infra failure returns 500",
			body:          validBody,
			svc:           &fakeVerificationService{verifyErr: errors.New("db down")},
			wantStatus:    http.StatusInternalServerError,
			wantErrorCode: errcode.InternalError,
			wantReached:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := tc.svc
			if svc == nil {
				svc = &fakeVerificationService{}
			}
			g := gatewayWithVerification(svc)

			req := httptest.NewRequest(http.MethodPost, "/users/verifications", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			g.RegisterRoutes().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if svc.verifyCalled != tc.wantReached {
				t.Errorf("service reached = %v, want %v", svc.verifyCalled, tc.wantReached)
			}
			if tc.wantErrorCode != 0 {
				resp := decodeErrorResponse(t, rec.Body)
				if resp.Error.Code != tc.wantErrorCode {
					t.Errorf("error.code: got %d, want %d", resp.Error.Code, tc.wantErrorCode)
				}
			}
		})
	}
}

func TestHandleResendVerification(t *testing.T) {
	const validBody = `{"mail_address":"alice@example.com"}`

	tests := []struct {
		name          string
		body          string
		svc           *fakeVerificationService
		wantStatus    int
		wantErrorCode errcode.Code
		wantReached   bool
	}{
		{name: "valid resend returns 200", body: validBody, svc: &fakeVerificationService{}, wantStatus: http.StatusOK, wantReached: true},
		{name: "unknown/active still 200 (uniform)", body: validBody, svc: &fakeVerificationService{}, wantStatus: http.StatusOK, wantReached: true},
		{name: "empty body", body: ``, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "missing mail_address", body: `{}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "invalid email", body: `{"mail_address":"nope"}`, wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidEmail},
		{
			name:          "rate limited returns 429",
			body:          validBody,
			svc:           &fakeVerificationService{resendErr: persistence.ErrVerificationRateLimited},
			wantStatus:    http.StatusTooManyRequests,
			wantErrorCode: errcode.VerificationRateLimited,
			wantReached:   true,
		},
		{
			name:          "infra failure returns 500",
			body:          validBody,
			svc:           &fakeVerificationService{resendErr: errors.New("db down")},
			wantStatus:    http.StatusInternalServerError,
			wantErrorCode: errcode.InternalError,
			wantReached:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := tc.svc
			if svc == nil {
				svc = &fakeVerificationService{}
			}
			g := gatewayWithVerification(svc)

			req := httptest.NewRequest(http.MethodPost, "/users/verifications/resend", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			g.RegisterRoutes().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if svc.resendCalled != tc.wantReached {
				t.Errorf("service reached = %v, want %v", svc.resendCalled, tc.wantReached)
			}
			if tc.wantErrorCode != 0 {
				resp := decodeErrorResponse(t, rec.Body)
				if resp.Error.Code != tc.wantErrorCode {
					t.Errorf("error.code: got %d, want %d", resp.Error.Code, tc.wantErrorCode)
				}
			}
		})
	}
}

func TestHandleVerify_WrongMethodReturns405(t *testing.T) {
	g := gatewayWithVerification(&fakeVerificationService{})
	for _, path := range []string{"/users/verifications", "/users/verifications/resend"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		g.RegisterRoutes().ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s GET: status %d, want 405", path, rec.Code)
		}
	}
}

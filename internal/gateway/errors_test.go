package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	commonpb "github.com/oklahomer/blabby/gen/common"
	"github.com/oklahomer/blabby/internal/errcode"
)

func TestErrorResponseJSONSerialization(t *testing.T) {
	detail := ErrorDetail{
		Code:    2001,
		Status:  "ROOM_NOT_MEMBER",
		Message: "You are not a member of this room",
	}
	resp := ErrorResponse{Error: detail}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal ErrorResponse: %v", err)
	}

	// Verify the envelope shape
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal into map: %v", err)
	}

	errObj, ok := raw["error"]
	if !ok {
		t.Fatal("expected top-level 'error' key in JSON")
	}

	if code, ok := errObj["code"].(float64); !ok || int(code) != 2001 {
		t.Errorf("expected code 2001, got %v", errObj["code"])
	}
	if status, ok := errObj["status"].(string); !ok || status != "ROOM_NOT_MEMBER" {
		t.Errorf("expected status ROOM_NOT_MEMBER, got %v", errObj["status"])
	}
	if msg, ok := errObj["message"].(string); !ok || msg != "You are not a member of this room" {
		t.Errorf("expected message 'You are not a member of this room', got %v", errObj["message"])
	}

	// Verify exact JSON structure (no extra fields)
	keys := make(map[string]bool)
	for k := range errObj {
		keys[k] = true
	}
	if len(keys) != 3 {
		t.Errorf("expected exactly 3 fields in error object, got %d: %v", len(keys), keys)
	}
}

func TestNewErrorDetail(t *testing.T) {
	detail := NewErrorDetail(errcode.AuthInvalidToken, "invalid token")

	if detail.Code != 1001 {
		t.Errorf("expected code 1001, got %d", detail.Code)
	}
	if detail.Status != "AUTH_INVALID_TOKEN" {
		t.Errorf("expected status AUTH_INVALID_TOKEN, got %s", detail.Status)
	}
	if detail.Message != "invalid token" {
		t.Errorf("expected message 'invalid token', got %s", detail.Message)
	}
}

func TestWriteErrorResponse(t *testing.T) {
	tests := []struct {
		name           string
		httpStatus     int
		detail         ErrorDetail
		wantCode       errcode.Code
		wantStatus     string
		wantMessage    string
		wantHTTPStatus int
	}{
		{
			name:           "unauthorized error",
			httpStatus:     http.StatusUnauthorized,
			detail:         NewErrorDetail(errcode.AuthInvalidToken, "token is invalid"),
			wantCode:       1001,
			wantStatus:     "AUTH_INVALID_TOKEN",
			wantMessage:    "token is invalid",
			wantHTTPStatus: http.StatusUnauthorized,
		},
		{
			name:           "not found error",
			httpStatus:     http.StatusNotFound,
			detail:         NewErrorDetail(errcode.RoomNotFound, "room does not exist"),
			wantCode:       2003,
			wantStatus:     "ROOM_NOT_FOUND",
			wantMessage:    "room does not exist",
			wantHTTPStatus: http.StatusNotFound,
		},
		{
			name:           "internal error",
			httpStatus:     http.StatusInternalServerError,
			detail:         NewErrorDetail(errcode.InternalError, "an internal error occurred"),
			wantCode:       5001,
			wantStatus:     "INTERNAL_ERROR",
			wantMessage:    "an internal error occurred",
			wantHTTPStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteErrorResponse(rec, tt.httpStatus, tt.detail)

			// Check HTTP status code
			if rec.Code != tt.wantHTTPStatus {
				t.Errorf("HTTP status = %d, want %d", rec.Code, tt.wantHTTPStatus)
			}

			// Check Content-Type header
			ct := rec.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Errorf("Content-Type = %q, want %q", ct, "application/json")
			}

			// Check JSON body
			var resp ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to unmarshal response body: %v", err)
			}

			if resp.Error.Code != tt.wantCode {
				t.Errorf("response code = %d, want %d", resp.Error.Code, tt.wantCode)
			}
			if resp.Error.Status != tt.wantStatus {
				t.Errorf("response status = %q, want %q", resp.Error.Status, tt.wantStatus)
			}
			if resp.Error.Message != tt.wantMessage {
				t.Errorf("response message = %q, want %q", resp.Error.Message, tt.wantMessage)
			}
		})
	}
}

func TestFromProtoErrorDetail(t *testing.T) {
	t.Run("converts all fields", func(t *testing.T) {
		proto := &commonpb.ErrorDetail{
			Code:    2001,
			Status:  "ROOM_NOT_MEMBER",
			Message: "You are not a member of this room",
		}

		detail, err := FromProtoErrorDetail(proto)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if detail.Code != 2001 {
			t.Errorf("code = %d, want 2001", detail.Code)
		}
		if detail.Status != "ROOM_NOT_MEMBER" {
			t.Errorf("status = %q, want %q", detail.Status, "ROOM_NOT_MEMBER")
		}
		if detail.Message != "You are not a member of this room" {
			t.Errorf("message = %q, want %q", detail.Message, "You are not a member of this room")
		}
	})

	t.Run("returns error for nil input", func(t *testing.T) {
		_, err := FromProtoErrorDetail(nil)
		if err == nil {
			t.Fatal("expected error for nil proto, got nil")
		}
		if !errors.Is(err, ErrNilProtoErrorDetail) {
			t.Errorf("error = %v, want %v", err, ErrNilProtoErrorDetail)
		}
	})

	tests := []struct {
		name      string
		proto     *commonpb.ErrorDetail
		wantCause error
	}{
		{
			name:      "rejects mismatched status",
			proto:     &commonpb.ErrorDetail{Code: 2001, Status: "ROOM_NOT_FOUND", Message: "bad pair"},
			wantCause: errcode.ErrStatusMismatch,
		},
		{
			name:      "rejects unknown code",
			proto:     &commonpb.ErrorDetail{Code: 9999, Status: "UNKNOWN_ERROR", Message: "bad code"},
			wantCause: errcode.ErrUnknownCode,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromProtoErrorDetail(tt.proto)
			if !errors.Is(err, ErrInvalidProtoErrorDetail) {
				t.Errorf("error = %v, want %v", err, ErrInvalidProtoErrorDetail)
			}
			if !errors.Is(err, tt.wantCause) {
				t.Errorf("error = %v, want cause %v", err, tt.wantCause)
			}
		})
	}
}

func TestHTTPStatus(t *testing.T) {
	tests := []struct {
		name string
		code errcode.Code
		want int
	}{
		{"AuthInvalidToken→401", errcode.AuthInvalidToken, http.StatusUnauthorized},
		{"AuthExpiredToken→401", errcode.AuthExpiredToken, http.StatusUnauthorized},
		{"AuthMissingToken→401", errcode.AuthMissingToken, http.StatusUnauthorized},
		{"RoomNotMember→403", errcode.RoomNotMember, http.StatusForbidden},
		{"RoomAlreadyMember→409", errcode.RoomAlreadyMember, http.StatusConflict},
		{"RoomNotFound→404", errcode.RoomNotFound, http.StatusNotFound},
		{"RoomOwnerCannotLeave→409", errcode.RoomOwnerCannotLeave, http.StatusConflict},
		{"RoomPermissionDenied→403", errcode.RoomPermissionDenied, http.StatusForbidden},
		{"RateLimitExceeded→429", errcode.RateLimitExceeded, http.StatusTooManyRequests},
		{"VerificationRateLimited→429", errcode.VerificationRateLimited, http.StatusTooManyRequests},
		{"InvalidRequest→400", errcode.InvalidRequest, http.StatusBadRequest},
		{"MissingField→400", errcode.MissingField, http.StatusBadRequest},
		{"PayloadTooLarge→413", errcode.PayloadTooLarge, http.StatusRequestEntityTooLarge},
		{"InvalidEmail→400", errcode.InvalidEmail, http.StatusBadRequest},
		{"WeakPassword→400", errcode.WeakPassword, http.StatusBadRequest},
		{"VerificationInvalid→400", errcode.VerificationInvalid, http.StatusBadRequest},
		{"EmailAlreadyRegistered→409", errcode.EmailAlreadyRegistered, http.StatusConflict},
		{"HandleAlreadyTaken→409", errcode.HandleAlreadyTaken, http.StatusConflict},
		{"InternalError→500", errcode.InternalError, http.StatusInternalServerError},
		{"ServiceUnavailable→503", errcode.ServiceUnavailable, http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := httpStatus(tt.code); got != tt.want {
				t.Errorf("httpStatus(%d) = %d, want %d", tt.code, got, tt.want)
			}
		})
	}

	t.Run("unknown code falls through to 500", func(t *testing.T) {
		if got := httpStatus(errcode.Code(9999)); got != http.StatusInternalServerError {
			t.Errorf("httpStatus(9999) = %d, want %d", got, http.StatusInternalServerError)
		}
	})
}

func TestConvenienceErrorFunctions(t *testing.T) {
	tests := []struct {
		name       string
		fn         func(string) ErrorDetail
		wantCode   errcode.Code
		wantStatus string
	}{
		{"ErrAuthInvalidToken", ErrAuthInvalidToken, 1001, "AUTH_INVALID_TOKEN"},
		{"ErrAuthExpiredToken", ErrAuthExpiredToken, 1002, "AUTH_EXPIRED_TOKEN"},
		{"ErrAuthMissingToken", ErrAuthMissingToken, 1003, "AUTH_MISSING_TOKEN"},
		{"ErrRoomNotMember", ErrRoomNotMember, 2001, "ROOM_NOT_MEMBER"},
		{"ErrRoomAlreadyMember", ErrRoomAlreadyMember, 2002, "ROOM_ALREADY_MEMBER"},
		{"ErrRoomNotFound", ErrRoomNotFound, 2003, "ROOM_NOT_FOUND"},
		{"ErrRateLimitExceeded", ErrRateLimitExceeded, 3001, "RATE_LIMIT_EXCEEDED"},
		{"ErrInvalidRequest", ErrInvalidRequest, 4001, "INVALID_REQUEST"},
		{"ErrMissingField", ErrMissingField, 4002, "MISSING_FIELD"},
		{"ErrPayloadTooLarge", ErrPayloadTooLarge, 4003, "PAYLOAD_TOO_LARGE"},
		{"ErrInternalError", ErrInternalError, 5001, "INTERNAL_ERROR"},
		{"ErrServiceUnavailable", ErrServiceUnavailable, 5002, "SERVICE_UNAVAILABLE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := tt.fn("test message")

			if detail.Code != tt.wantCode {
				t.Errorf("code = %d, want %d", detail.Code, tt.wantCode)
			}
			if detail.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", detail.Status, tt.wantStatus)
			}
			if detail.Message != "test message" {
				t.Errorf("message = %q, want %q", detail.Message, "test message")
			}
		})
	}
}

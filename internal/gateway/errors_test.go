package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	commonpb "github.com/oklahomer/blabby/gen/common"
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
	detail := NewErrorDetail(CodeAuthInvalidToken, "invalid token")

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

func TestErrorCodeConstants(t *testing.T) {
	tests := []struct {
		name string
		code ErrorCode
		want int
	}{
		{"CodeAuthInvalidToken", CodeAuthInvalidToken, 1001},
		{"CodeAuthExpiredToken", CodeAuthExpiredToken, 1002},
		{"CodeAuthMissingToken", CodeAuthMissingToken, 1003},
		{"CodeRoomNotMember", CodeRoomNotMember, 2001},
		{"CodeRoomAlreadyMember", CodeRoomAlreadyMember, 2002},
		{"CodeRoomNotFound", CodeRoomNotFound, 2003},
		{"CodeRateLimitExceeded", CodeRateLimitExceeded, 3001},
		{"CodeInvalidRequest", CodeInvalidRequest, 4001},
		{"CodeMissingField", CodeMissingField, 4002},
		{"CodePayloadTooLarge", CodePayloadTooLarge, 4003},
		{"CodeInternalError", CodeInternalError, 5001},
		{"CodeServiceUnavailable", CodeServiceUnavailable, 5002},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if int(tt.code) != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.code, tt.want)
			}
		})
	}
}

func TestErrorCodeStatus(t *testing.T) {
	tests := []struct {
		code ErrorCode
		want string
	}{
		{CodeAuthInvalidToken, "AUTH_INVALID_TOKEN"},
		{CodeAuthExpiredToken, "AUTH_EXPIRED_TOKEN"},
		{CodeAuthMissingToken, "AUTH_MISSING_TOKEN"},
		{CodeRoomNotMember, "ROOM_NOT_MEMBER"},
		{CodeRoomAlreadyMember, "ROOM_ALREADY_MEMBER"},
		{CodeRoomNotFound, "ROOM_NOT_FOUND"},
		{CodeRateLimitExceeded, "RATE_LIMIT_EXCEEDED"},
		{CodeInvalidRequest, "INVALID_REQUEST"},
		{CodeMissingField, "MISSING_FIELD"},
		{CodePayloadTooLarge, "PAYLOAD_TOO_LARGE"},
		{CodeInternalError, "INTERNAL_ERROR"},
		{CodeServiceUnavailable, "SERVICE_UNAVAILABLE"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.code.Status(); got != tt.want {
				t.Errorf("ErrorCode(%d).Status() = %q, want %q", tt.code, got, tt.want)
			}
		})
	}

	t.Run("unknown code", func(t *testing.T) {
		if got := ErrorCode(9999).Status(); got != "UNKNOWN_ERROR" {
			t.Errorf("ErrorCode(9999).Status() = %q, want %q", got, "UNKNOWN_ERROR")
		}
	})
}

func TestErrorCodeExhaustiveness(t *testing.T) {
	allCodes := []ErrorCode{
		CodeAuthInvalidToken, CodeAuthExpiredToken, CodeAuthMissingToken,
		CodeRoomNotMember, CodeRoomAlreadyMember, CodeRoomNotFound,
		CodeRateLimitExceeded,
		CodeInvalidRequest, CodeMissingField, CodePayloadTooLarge,
		CodeInternalError, CodeServiceUnavailable,
	}

	t.Run("all codes have a Status mapping", func(t *testing.T) {
		for _, code := range allCodes {
			if code.Status() == "UNKNOWN_ERROR" {
				t.Errorf("ErrorCode(%d) returns UNKNOWN_ERROR — missing case in Status()", code)
			}
		}
	})

	t.Run("all codes are unique", func(t *testing.T) {
		seen := make(map[ErrorCode]bool)
		for _, code := range allCodes {
			if seen[code] {
				t.Errorf("duplicate ErrorCode value: %d", code)
			}
			seen[code] = true
		}
	})
}

func TestWriteErrorResponse(t *testing.T) {
	tests := []struct {
		name           string
		httpStatus     int
		detail         ErrorDetail
		wantCode       int
		wantStatus     string
		wantMessage    string
		wantHTTPStatus int
	}{
		{
			name:           "unauthorized error",
			httpStatus:     http.StatusUnauthorized,
			detail:         NewErrorDetail(CodeAuthInvalidToken, "token is invalid"),
			wantCode:       1001,
			wantStatus:     "AUTH_INVALID_TOKEN",
			wantMessage:    "token is invalid",
			wantHTTPStatus: http.StatusUnauthorized,
		},
		{
			name:           "not found error",
			httpStatus:     http.StatusNotFound,
			detail:         NewErrorDetail(CodeRoomNotFound, "room does not exist"),
			wantCode:       2003,
			wantStatus:     "ROOM_NOT_FOUND",
			wantMessage:    "room does not exist",
			wantHTTPStatus: http.StatusNotFound,
		},
		{
			name:           "internal error",
			httpStatus:     http.StatusInternalServerError,
			detail:         NewErrorDetail(CodeInternalError, "an internal error occurred"),
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
}

func TestErrorCodeHTTPStatus(t *testing.T) {
	tests := []struct {
		name string
		code ErrorCode
		want int
	}{
		{"AuthInvalidToken→401", CodeAuthInvalidToken, http.StatusUnauthorized},
		{"AuthExpiredToken→401", CodeAuthExpiredToken, http.StatusUnauthorized},
		{"AuthMissingToken→401", CodeAuthMissingToken, http.StatusUnauthorized},
		{"RoomNotMember→403", CodeRoomNotMember, http.StatusForbidden},
		{"RoomAlreadyMember→409", CodeRoomAlreadyMember, http.StatusConflict},
		{"RoomNotFound→404", CodeRoomNotFound, http.StatusNotFound},
		{"RateLimitExceeded→429", CodeRateLimitExceeded, http.StatusTooManyRequests},
		{"InvalidRequest→400", CodeInvalidRequest, http.StatusBadRequest},
		{"MissingField→400", CodeMissingField, http.StatusBadRequest},
		{"PayloadTooLarge→413", CodePayloadTooLarge, http.StatusRequestEntityTooLarge},
		{"InternalError→500", CodeInternalError, http.StatusInternalServerError},
		{"ServiceUnavailable→503", CodeServiceUnavailable, http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.code.HTTPStatus(); got != tt.want {
				t.Errorf("ErrorCode(%d).HTTPStatus() = %d, want %d", tt.code, got, tt.want)
			}
		})
	}

	t.Run("unknown code falls through to 500", func(t *testing.T) {
		if got := ErrorCode(9999).HTTPStatus(); got != http.StatusInternalServerError {
			t.Errorf("ErrorCode(9999).HTTPStatus() = %d, want %d", got, http.StatusInternalServerError)
		}
	})
}

func TestConvenienceErrorFunctions(t *testing.T) {
	tests := []struct {
		name       string
		fn         func(string) ErrorDetail
		wantCode   int
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

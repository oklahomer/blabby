package errcode_test

import (
	"errors"
	"testing"

	"github.com/oklahomer/blabby/internal/errcode"
)

var taxonomyCases = []struct {
	code   errcode.Code
	value  int32
	status string
}{
	{errcode.AuthInvalidToken, 1001, "AUTH_INVALID_TOKEN"},
	{errcode.AuthExpiredToken, 1002, "AUTH_EXPIRED_TOKEN"},
	{errcode.AuthMissingToken, 1003, "AUTH_MISSING_TOKEN"},
	{errcode.AuthAccountPending, 1004, "AUTH_ACCOUNT_PENDING"},
	{errcode.RoomNotMember, 2001, "ROOM_NOT_MEMBER"},
	{errcode.RoomAlreadyMember, 2002, "ROOM_ALREADY_MEMBER"},
	{errcode.RoomNotFound, 2003, "ROOM_NOT_FOUND"},
	{errcode.RoomOwnerCannotLeave, 2004, "ROOM_OWNER_CANNOT_LEAVE"},
	{errcode.RoomPermissionDenied, 2005, "ROOM_PERMISSION_DENIED"},
	{errcode.RateLimitExceeded, 3001, "RATE_LIMIT_EXCEEDED"},
	{errcode.VerificationRateLimited, 3002, "VERIFICATION_RATE_LIMITED"},
	{errcode.InvalidRequest, 4001, "INVALID_REQUEST"},
	{errcode.MissingField, 4002, "MISSING_FIELD"},
	{errcode.PayloadTooLarge, 4003, "PAYLOAD_TOO_LARGE"},
	{errcode.InvalidEmail, 4004, "INVALID_EMAIL"},
	{errcode.WeakPassword, 4005, "WEAK_PASSWORD"},
	{errcode.InternalError, 5001, "INTERNAL_ERROR"},
	{errcode.ServiceUnavailable, 5002, "SERVICE_UNAVAILABLE"},
	{errcode.EmailAlreadyRegistered, 6001, "EMAIL_ALREADY_REGISTERED"},
	{errcode.HandleAlreadyTaken, 6002, "HANDLE_ALREADY_TAKEN"},
	{errcode.VerificationInvalid, 6003, "VERIFICATION_INVALID"},
}

// TestCodeTaxonomy pins every code's numeric value and canonical status string.
// It is the regression guard for the single source of truth: a change to a code
// or its status here is deliberate and visible, and every consumer inherits it.
func TestCodeTaxonomy(t *testing.T) {
	t.Parallel()
	for _, c := range taxonomyCases {
		if got := c.code.Int32(); got != c.value {
			t.Errorf("%s: Int32() = %d, want %d", c.status, got, c.value)
		}
		if got := c.code.Status(); got != c.status {
			t.Errorf("code %d: Status() = %q, want %q", c.value, got, c.status)
		}
	}
}

func TestUnknownCodeStatus(t *testing.T) {
	t.Parallel()
	if got := errcode.Code(9999).Status(); got != "UNKNOWN_ERROR" {
		t.Errorf("Code(9999).Status() = %q, want UNKNOWN_ERROR", got)
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	for _, tc := range taxonomyCases {
		t.Run(tc.status, func(t *testing.T) {
			got, err := errcode.Parse(tc.value, tc.status)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got != tc.code {
				t.Errorf("Parse = %d, want %d", got, tc.code)
			}
		})
	}

	tests := []struct {
		name   string
		code   int32
		status string
		want   error
	}{
		{name: "mismatched status", code: errcode.RoomNotMember.Int32(), status: "ROOM_NOT_FOUND", want: errcode.ErrStatusMismatch},
		{name: "empty status", code: errcode.RoomNotMember.Int32(), status: "", want: errcode.ErrStatusMismatch},
		{name: "unknown code", code: 9999, status: "UNKNOWN_ERROR", want: errcode.ErrUnknownCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := errcode.Parse(tt.code, tt.status); !errors.Is(err, tt.want) {
				t.Errorf("Parse(%d, %q) error = %v, want %v", tt.code, tt.status, err, tt.want)
			}
		})
	}
}

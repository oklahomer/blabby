package errcode_test

import (
	"errors"
	"testing"

	"github.com/oklahomer/blabby/internal/errcode"
)

// TestCodeTaxonomy pins every code's numeric value and canonical status string.
// It is the regression guard for the single source of truth: a change to a code
// or its status here is deliberate and visible, and every consumer inherits it.
func TestCodeTaxonomy(t *testing.T) {
	cases := []struct {
		code   errcode.Code
		value  int32
		status string
	}{
		{errcode.AuthInvalidToken, 1001, "AUTH_INVALID_TOKEN"},
		{errcode.AuthExpiredToken, 1002, "AUTH_EXPIRED_TOKEN"},
		{errcode.AuthMissingToken, 1003, "AUTH_MISSING_TOKEN"},
		{errcode.RoomNotMember, 2001, "ROOM_NOT_MEMBER"},
		{errcode.RoomAlreadyMember, 2002, "ROOM_ALREADY_MEMBER"},
		{errcode.RoomNotFound, 2003, "ROOM_NOT_FOUND"},
		{errcode.RateLimitExceeded, 3001, "RATE_LIMIT_EXCEEDED"},
		{errcode.InvalidRequest, 4001, "INVALID_REQUEST"},
		{errcode.MissingField, 4002, "MISSING_FIELD"},
		{errcode.PayloadTooLarge, 4003, "PAYLOAD_TOO_LARGE"},
		{errcode.InternalError, 5001, "INTERNAL_ERROR"},
		{errcode.ServiceUnavailable, 5002, "SERVICE_UNAVAILABLE"},
	}
	for _, c := range cases {
		if got := c.code.Int32(); got != c.value {
			t.Errorf("%s: Int32() = %d, want %d", c.status, got, c.value)
		}
		if got := c.code.Status(); got != c.status {
			t.Errorf("code %d: Status() = %q, want %q", c.value, got, c.status)
		}
	}
}

func TestUnknownCodeStatus(t *testing.T) {
	if got := errcode.Code(9999).Status(); got != "UNKNOWN_ERROR" {
		t.Errorf("Code(9999).Status() = %q, want UNKNOWN_ERROR", got)
	}
}

func TestParse(t *testing.T) {
	valid := []errcode.Code{
		errcode.AuthInvalidToken,
		errcode.AuthExpiredToken,
		errcode.AuthMissingToken,
		errcode.RoomNotMember,
		errcode.RoomAlreadyMember,
		errcode.RoomNotFound,
		errcode.RateLimitExceeded,
		errcode.InvalidRequest,
		errcode.MissingField,
		errcode.PayloadTooLarge,
		errcode.InternalError,
		errcode.ServiceUnavailable,
	}
	for _, want := range valid {
		t.Run(want.Status(), func(t *testing.T) {
			got, err := errcode.Parse(want.Int32(), want.Status())
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got != want {
				t.Errorf("Parse = %d, want %d", got, want)
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

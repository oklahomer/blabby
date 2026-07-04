package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	commonpb "github.com/oklahomer/blabby/gen/common"
	"github.com/oklahomer/blabby/internal/errcode"
)

// httpStatus returns the canonical HTTP status for a shared error code. It is
// the single source of truth for the gateway's code → HTTP mapping;
// every handler that translates a grain error into an HTTP response
// calls it so the mapping cannot drift across endpoints.
func httpStatus(code errcode.Code) int {
	switch code {
	case errcode.AuthInvalidToken, errcode.AuthExpiredToken, errcode.AuthMissingToken,
		errcode.AuthAccountPending:
		return http.StatusUnauthorized
	case errcode.RoomNotMember, errcode.RoomPermissionDenied:
		return http.StatusForbidden
	case errcode.RoomAlreadyMember, errcode.RoomOwnerCannotLeave,
		errcode.EmailAlreadyRegistered, errcode.HandleAlreadyTaken:
		return http.StatusConflict
	case errcode.RoomNotFound:
		return http.StatusNotFound
	case errcode.RateLimitExceeded, errcode.VerificationRateLimited:
		return http.StatusTooManyRequests
	case errcode.InvalidRequest, errcode.MissingField, errcode.InvalidEmail, errcode.WeakPassword,
		errcode.VerificationInvalid:
		return http.StatusBadRequest
	case errcode.PayloadTooLarge:
		return http.StatusRequestEntityTooLarge
	case errcode.ServiceUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// ErrorDetail holds the error information for an API error response.
type ErrorDetail struct {
	Code    errcode.Code `json:"code"`
	Status  string       `json:"status"`
	Message string       `json:"message"`
}

// ErrorResponse is the top-level JSON envelope wrapping ErrorDetail.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// NewErrorDetail constructs an ErrorDetail, deriving status from the shared code.
func NewErrorDetail(code errcode.Code, message string) ErrorDetail {
	return ErrorDetail{
		Code:    code,
		Status:  code.Status(),
		Message: message,
	}
}

// WriteErrorResponse writes a JSON error envelope to the response writer
// with the given HTTP status code. This should be the only path for writing
// error responses, ensuring no internal details leak to clients.
func WriteErrorResponse(w http.ResponseWriter, httpStatus int, detail ErrorDetail) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	if err := json.NewEncoder(w).Encode(ErrorResponse{Error: detail}); err != nil {
		slog.Error("failed to write error response", "error", err, "code", detail.Code, "status", detail.Status)
	}
}

// ErrNilProtoErrorDetail is returned when FromProtoErrorDetail receives a nil proto,
// indicating a broken grain contract (failure reported without error details).
var ErrNilProtoErrorDetail = errors.New("proto ErrorDetail is nil")

// ErrInvalidProtoErrorDetail is returned when a protobuf ErrorDetail carries
// an unknown code or a status that does not match its code.
var ErrInvalidProtoErrorDetail = errors.New("proto ErrorDetail has invalid taxonomy")

// FromProtoErrorDetail converts a protobuf ErrorDetail to a gateway ErrorDetail.
// It returns an error if the proto is nil or its raw code/status pair does not
// parse into the shared taxonomy.
func FromProtoErrorDetail(proto *commonpb.ErrorDetail) (ErrorDetail, error) {
	if proto == nil {
		return ErrorDetail{}, ErrNilProtoErrorDetail
	}
	code, err := errcode.Parse(proto.GetCode(), proto.GetStatus())
	if err != nil {
		return ErrorDetail{}, fmt.Errorf("%w: %w", ErrInvalidProtoErrorDetail, err)
	}
	return NewErrorDetail(code, proto.GetMessage()), nil
}

// Convenience constructors for common errors. Each pairs a fixed code with
// NewErrorDetail so call sites supply only the client-facing message and cannot
// mismatch a code with the wrong status.

// ErrAuthInvalidToken builds an ErrorDetail for a token that failed validation.
func ErrAuthInvalidToken(msg string) ErrorDetail {
	return NewErrorDetail(errcode.AuthInvalidToken, msg)
}

// ErrAuthExpiredToken builds an ErrorDetail for an expired authentication token.
func ErrAuthExpiredToken(msg string) ErrorDetail {
	return NewErrorDetail(errcode.AuthExpiredToken, msg)
}

// ErrAuthMissingToken builds an ErrorDetail for a request carrying no auth token.
func ErrAuthMissingToken(msg string) ErrorDetail {
	return NewErrorDetail(errcode.AuthMissingToken, msg)
}

// ErrAuthAccountPending builds an ErrorDetail for a correct-password login
// against an account that has not completed email verification.
func ErrAuthAccountPending(msg string) ErrorDetail {
	return NewErrorDetail(errcode.AuthAccountPending, msg)
}

// ErrRoomNotMember builds an ErrorDetail for an action that requires a room
// membership the caller does not hold.
func ErrRoomNotMember(msg string) ErrorDetail { return NewErrorDetail(errcode.RoomNotMember, msg) }

// ErrRoomAlreadyMember builds an ErrorDetail for joining a room the caller has
// already joined.
func ErrRoomAlreadyMember(msg string) ErrorDetail {
	return NewErrorDetail(errcode.RoomAlreadyMember, msg)
}

// ErrRoomNotFound builds an ErrorDetail for a reference to an unknown room.
func ErrRoomNotFound(msg string) ErrorDetail { return NewErrorDetail(errcode.RoomNotFound, msg) }

// ErrRateLimitExceeded builds an ErrorDetail for a caller that exceeded its
// rate limit.
func ErrRateLimitExceeded(msg string) ErrorDetail {
	return NewErrorDetail(errcode.RateLimitExceeded, msg)
}

// ErrInvalidRequest builds an ErrorDetail for a malformed or semantically
// invalid request.
func ErrInvalidRequest(msg string) ErrorDetail { return NewErrorDetail(errcode.InvalidRequest, msg) }

// ErrMissingField builds an ErrorDetail for a request missing a required field.
func ErrMissingField(msg string) ErrorDetail { return NewErrorDetail(errcode.MissingField, msg) }

// ErrPayloadTooLarge builds an ErrorDetail for a request body over the size limit.
func ErrPayloadTooLarge(msg string) ErrorDetail { return NewErrorDetail(errcode.PayloadTooLarge, msg) }

// ErrInternalError builds an ErrorDetail for an unexpected server-side failure.
func ErrInternalError(msg string) ErrorDetail { return NewErrorDetail(errcode.InternalError, msg) }

// ErrServiceUnavailable builds an ErrorDetail for a temporarily unavailable
// dependency.
func ErrServiceUnavailable(msg string) ErrorDetail {
	return NewErrorDetail(errcode.ServiceUnavailable, msg)
}

// ErrInvalidEmail builds an ErrorDetail for a malformed registration email.
func ErrInvalidEmail(msg string) ErrorDetail { return NewErrorDetail(errcode.InvalidEmail, msg) }

// ErrWeakPassword builds an ErrorDetail for a registration password below the
// minimum strength.
func ErrWeakPassword(msg string) ErrorDetail { return NewErrorDetail(errcode.WeakPassword, msg) }

// ErrEmailAlreadyRegistered builds an ErrorDetail for a registration whose email
// is already taken.
func ErrEmailAlreadyRegistered(msg string) ErrorDetail {
	return NewErrorDetail(errcode.EmailAlreadyRegistered, msg)
}

// ErrHandleAlreadyTaken builds an ErrorDetail for a registration whose handle is
// already taken.
func ErrHandleAlreadyTaken(msg string) ErrorDetail {
	return NewErrorDetail(errcode.HandleAlreadyTaken, msg)
}

// ErrVerificationRateLimited builds an ErrorDetail for a PIN resend that exceeded
// its budget.
func ErrVerificationRateLimited(msg string) ErrorDetail {
	return NewErrorDetail(errcode.VerificationRateLimited, msg)
}

// ErrVerificationInvalid builds an ErrorDetail for a verification that could not
// succeed (unknown account/challenge, expired challenge, or wrong PIN).
func ErrVerificationInvalid(msg string) ErrorDetail {
	return NewErrorDetail(errcode.VerificationInvalid, msg)
}

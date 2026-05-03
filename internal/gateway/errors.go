package gateway

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	commonpb "github.com/oklahomer/blabby/gen/common"
)

// ErrorCode is a typed error code for programmatic error handling.
type ErrorCode int

// Error codes — Authentication (1000-1999)
const (
	CodeAuthInvalidToken ErrorCode = 1001
	CodeAuthExpiredToken ErrorCode = 1002
	CodeAuthMissingToken ErrorCode = 1003
)

// Error codes — Room / Membership (2000-2999)
const (
	CodeRoomNotMember     ErrorCode = 2001
	CodeRoomAlreadyMember ErrorCode = 2002
	CodeRoomNotFound      ErrorCode = 2003
)

// Error codes — Rate Limiting (3000-3999)
const (
	CodeRateLimitExceeded ErrorCode = 3001
)

// Error codes — Validation (4000-4999)
const (
	CodeInvalidRequest ErrorCode = 4001
	CodeMissingField   ErrorCode = 4002
)

// Error codes — System / Internal (5000-5999)
const (
	CodeInternalError      ErrorCode = 5001
	CodeServiceUnavailable ErrorCode = 5002
)

// Status returns the canonical status string for the error code.
func (c ErrorCode) Status() string {
	switch c {
	case CodeAuthInvalidToken:
		return "AUTH_INVALID_TOKEN"
	case CodeAuthExpiredToken:
		return "AUTH_EXPIRED_TOKEN"
	case CodeAuthMissingToken:
		return "AUTH_MISSING_TOKEN"
	case CodeRoomNotMember:
		return "ROOM_NOT_MEMBER"
	case CodeRoomAlreadyMember:
		return "ROOM_ALREADY_MEMBER"
	case CodeRoomNotFound:
		return "ROOM_NOT_FOUND"
	case CodeRateLimitExceeded:
		return "RATE_LIMIT_EXCEEDED"
	case CodeInvalidRequest:
		return "INVALID_REQUEST"
	case CodeMissingField:
		return "MISSING_FIELD"
	case CodeInternalError:
		return "INTERNAL_ERROR"
	case CodeServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	default:
		return "UNKNOWN_ERROR"
	}
}

// ErrorDetail holds the error information for an API error response.
type ErrorDetail struct {
	Code    int    `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ErrorResponse is the top-level JSON envelope wrapping ErrorDetail.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// NewErrorDetail constructs an ErrorDetail, deriving the status string from the ErrorCode.
func NewErrorDetail(code ErrorCode, message string) ErrorDetail {
	return ErrorDetail{
		Code:    int(code),
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

// FromProtoErrorDetail converts a protobuf ErrorDetail to a gateway ErrorDetail.
// It returns an error if the proto is nil, which indicates a grain reported failure
// without providing error details.
func FromProtoErrorDetail(proto *commonpb.ErrorDetail) (ErrorDetail, error) {
	if proto == nil {
		return ErrorDetail{}, ErrNilProtoErrorDetail
	}
	return ErrorDetail{
		Code:    int(proto.Code),
		Status:  proto.Status,
		Message: proto.Message,
	}, nil
}

// Convenience constructors for common errors.

func ErrAuthInvalidToken(msg string) ErrorDetail  { return NewErrorDetail(CodeAuthInvalidToken, msg) }
func ErrAuthExpiredToken(msg string) ErrorDetail  { return NewErrorDetail(CodeAuthExpiredToken, msg) }
func ErrAuthMissingToken(msg string) ErrorDetail  { return NewErrorDetail(CodeAuthMissingToken, msg) }
func ErrRoomNotMember(msg string) ErrorDetail     { return NewErrorDetail(CodeRoomNotMember, msg) }
func ErrRoomAlreadyMember(msg string) ErrorDetail { return NewErrorDetail(CodeRoomAlreadyMember, msg) }
func ErrRoomNotFound(msg string) ErrorDetail      { return NewErrorDetail(CodeRoomNotFound, msg) }
func ErrRateLimitExceeded(msg string) ErrorDetail { return NewErrorDetail(CodeRateLimitExceeded, msg) }
func ErrInvalidRequest(msg string) ErrorDetail    { return NewErrorDetail(CodeInvalidRequest, msg) }
func ErrMissingField(msg string) ErrorDetail      { return NewErrorDetail(CodeMissingField, msg) }
func ErrInternalError(msg string) ErrorDetail     { return NewErrorDetail(CodeInternalError, msg) }
func ErrServiceUnavailable(msg string) ErrorDetail {
	return NewErrorDetail(CodeServiceUnavailable, msg)
}

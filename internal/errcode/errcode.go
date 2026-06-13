// Package errcode is the single source of truth for blabby's error taxonomy:
// each numeric error code paired with its canonical status string. The gateway
// and the grains both consume it so the code and its status string cannot drift
// apart across packages.
//
// The HTTP-status mapping is intentionally NOT here — that is a transport
// concern owned by the gateway (internal/gateway). The codes here are
// protocol-neutral and travel on the wire as proto ErrorDetail fields and in
// the gateway's JSON error envelope alike.
package errcode

import (
	"errors"
	"fmt"
)

// Code is a numeric error code. Its value and canonical status string are
// paired in status so every consumer uses the same taxonomy.
type Code int32

var (
	// ErrUnknownCode indicates that a raw boundary value is not part of the
	// shared error taxonomy.
	ErrUnknownCode = errors.New("unknown error code")
	// ErrStatusMismatch indicates that a raw status does not match the
	// canonical status paired with its numeric code.
	ErrStatusMismatch = errors.New("error status does not match code")
)

// The error taxonomy. Ranges group related failures: 1xxx authentication,
// 2xxx room/membership, 3xxx rate limiting, 4xxx validation, 5xxx system.
const (
	AuthInvalidToken   Code = 1001
	AuthExpiredToken   Code = 1002
	AuthMissingToken   Code = 1003
	RoomNotMember      Code = 2001
	RoomAlreadyMember  Code = 2002
	RoomNotFound       Code = 2003
	RateLimitExceeded  Code = 3001
	InvalidRequest     Code = 4001
	MissingField       Code = 4002
	PayloadTooLarge    Code = 4003
	InternalError      Code = 5001
	ServiceUnavailable Code = 5002
)

// status returns the canonical status string and whether c is recognized.
// Keeping recognition and projection in one switch prevents those concepts
// from drifting apart.
func (c Code) status() (string, bool) {
	switch c {
	case AuthInvalidToken:
		return "AUTH_INVALID_TOKEN", true
	case AuthExpiredToken:
		return "AUTH_EXPIRED_TOKEN", true
	case AuthMissingToken:
		return "AUTH_MISSING_TOKEN", true
	case RoomNotMember:
		return "ROOM_NOT_MEMBER", true
	case RoomAlreadyMember:
		return "ROOM_ALREADY_MEMBER", true
	case RoomNotFound:
		return "ROOM_NOT_FOUND", true
	case RateLimitExceeded:
		return "RATE_LIMIT_EXCEEDED", true
	case InvalidRequest:
		return "INVALID_REQUEST", true
	case MissingField:
		return "MISSING_FIELD", true
	case PayloadTooLarge:
		return "PAYLOAD_TOO_LARGE", true
	case InternalError:
		return "INTERNAL_ERROR", true
	case ServiceUnavailable:
		return "SERVICE_UNAVAILABLE", true
	default:
		return "", false
	}
}

// Status returns the canonical status string for the code, or "UNKNOWN_ERROR"
// for an unrecognized value.
func (c Code) Status() string {
	status, ok := c.status()
	if !ok {
		return "UNKNOWN_ERROR"
	}
	return status
}

// Parse converts a raw wire pair into a Code. It rejects unknown numeric codes
// and status strings that do not match the canonical taxonomy entry.
func Parse(value int32, status string) (Code, error) {
	code := Code(value)
	want, ok := code.status()
	if !ok {
		return 0, fmt.Errorf("%w: %d", ErrUnknownCode, value)
	}
	if status != want {
		return 0, fmt.Errorf("%w: code %d has status %q, want %q", ErrStatusMismatch, value, status, want)
	}
	return code, nil
}

// Int32 returns the code as an int32 for building proto ErrorDetail messages,
// whose Code field is an int32.
func (c Code) Int32() int32 { return int32(c) }

package connection

import (
	"errors"
	"strings"
	"time"
)

var errEmptyAuthToken = errors.New("auth token must not be empty")

// AuthToken is a parsed authentication token received from the WebSocket
// boundary. It proves only that a token string was present after trimming;
// cryptographic validation belongs to auth.Authenticator.
type AuthToken struct {
	value string
}

func NewAuthToken(value string) (AuthToken, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return AuthToken{}, errEmptyAuthToken
	}
	return AuthToken{value: value}, nil
}

func (t AuthToken) String() string {
	return t.value
}

var errEmptyUserID = errors.New("user id must not be empty")

// UserID is the authenticated subject this connection registered under.
type UserID struct {
	value string
}

func NewUserID(value string) (UserID, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return UserID{}, errEmptyUserID
	}
	return UserID{value: value}, nil
}

func (id UserID) String() string {
	return id.value
}

// Inbound protocol messages.
type InboundAuth struct {
	Token AuthToken
}

type AppPongReceived struct{}

// Outbound protocol messages.
type AuthSucceeded struct{}

type AuthFailed struct {
	Code    int32
	Status  string
	Message string
}

type ChatDelivered struct {
	RoomID    string
	SenderID  string
	Text      string
	Timestamp time.Time
}

type RoomJoined struct {
	RoomID string
	UserID string
}

type RoomLeft struct {
	RoomID string
	UserID string
}

type ErrorResponse struct {
	Code    int32
	Status  string
	Message string
}

type AppPing struct{}

type CloseConnection struct {
	Reason string
}

// Internal control messages.
type AuthTimeoutExpired struct{}

type AppPingTick struct{}

type PongTimeoutExpired struct{}

type DecodeFailureReason string

const (
	decodeFailureMalformedJSON DecodeFailureReason = "malformed_json"
	decodeFailureUnknownType   DecodeFailureReason = "unknown_type"
)

type DecodeFailed struct {
	Reason DecodeFailureReason
}

type ProtocolViolationReason string

const (
	protocolViolationMissingToken ProtocolViolationReason = "missing_token"
)

type ProtocolViolation struct {
	Reason ProtocolViolationReason
}

type ConnectionClosed struct {
	Reason string
}

type ReadPumpPanicked struct {
	Cause string
}

type WritePumpFailed struct {
	Reason    string
	EventKind string
}

type WritePumpClosed struct {
	// CloseFrameErr is non-empty when the close-frame WriteControl returned
	// an error. Empty means the close frame was written successfully (or
	// gorilla wrote it without surfacing an error). Used for diagnostics
	// only — the actor stops regardless of the close-frame outcome.
	CloseFrameErr string
}

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

// UserRef carries a user's id and display name together as one value. It
// is a plain, non-validating relay shape: the data it holds was already
// parsed and validated by the Room grain (parseUserRef into id.UserRef)
// before reaching this actor, so re-checking the invariants here would
// only force handling an error that cannot occur. The grain RPC
// (commonpb.UserRef) and the domain value (id.UserRef) own the rules; this
// is the shape the connection relays toward the WebSocket.
type UserRef struct {
	ID   string
	Name string
}

type ChatDelivered struct {
	RoomID    string
	Sender    UserRef
	Text      string
	Timestamp time.Time
}

type RoomJoined struct {
	RoomID string
	User   UserRef
}

type RoomLeft struct {
	RoomID string
	User   UserRef
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

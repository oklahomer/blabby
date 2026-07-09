package connection

import (
	"errors"
	"strings"
	"time"

	"github.com/oklahomer/blabby/internal/errcode"
)

var errEmptyAuthToken = errors.New("auth token must not be empty")

// AuthToken is a parsed authentication token received from the WebSocket
// boundary. It proves only that a token string was present after trimming;
// cryptographic validation belongs to auth.Authenticator.
type AuthToken struct {
	value string
}

// NewAuthToken trims surrounding whitespace from value and returns the parsed
// token. It returns an error when the result is empty; presence after trimming
// is the only invariant it proves.
func NewAuthToken(value string) (AuthToken, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return AuthToken{}, errEmptyAuthToken
	}
	return AuthToken{value: value}, nil
}

// String returns the raw token value.
func (t AuthToken) String() string {
	return t.value
}

// Inbound protocol messages decoded from client WebSocket text frames.

// InboundAuth is the decoded "auth" frame carrying the parsed token. It is the
// only client message that makes progress in preAuthBehavior; a successful
// handleAuth promotes the connection to postAuthBehavior.
type InboundAuth struct {
	Token AuthToken
}

// AppPongReceived is the decoded "pong" frame, the client's reply to an
// [AppPing]. The heartbeat middleware owns the watchdog reset, so the actor
// treats it as a no-op: a pong neither establishes identity nor carries state.
type AppPongReceived struct{}

// Outbound protocol messages enqueued for the write pump, which encodes each
// into its WebSocket frame (see encodeOutboundMessage).

// AuthSucceeded signals that authentication passed and the connection is
// registered with the User grain. The write pump encodes it as the "auth_ok"
// frame.
type AuthSucceeded struct{}

// AuthFailed carries the rejection sent before a pre-auth connection closes.
// Code is a parsed shared error code; Message is a fixed, client-safe string.
// The write pump derives the numeric code and status for the "auth_error"
// frame.
type AuthFailed struct {
	Code    errcode.Code
	Message string
}

// UserRef carries a user's id and display name together as one value. It
// is a plain, non-validating relay shape: the data it holds was already
// parsed and validated by the Room grain (parseUserRef into domain.UserRef)
// before reaching this actor, so re-checking the invariants here would
// only force handling an error that cannot occur. The grain RPC
// (commonpb.UserRef) and the domain value (domain.UserRef) own the rules; this
// is the shape the connection relays toward the WebSocket. ID is the
// client-facing U… public code (never the internal numeric id).
type UserRef struct {
	ID   string
	Name string
}

// ChatDelivered is a room message fanned out to this user by the User grain.
// The write pump encodes it as the "message" frame; a zero Timestamp encodes
// as 0 (see timestampMillis).
type ChatDelivered struct {
	RoomID    string
	Sender    UserRef
	Text      string
	Timestamp time.Time
	// EventID is the message's decimal Snowflake timeline id, "" when the Room
	// grain ran storeless. The client orders and dedups the frame by it.
	EventID string
}

// RoomJoined reports that User joined RoomID. The write pump encodes it as the
// "joined" frame. EventID and At identify the durable member_joined timeline
// event so the client interleaves the system line with messages.
type RoomJoined struct {
	RoomID  string
	User    UserRef
	EventID string
	At      time.Time
}

// RoomLeft reports that User left RoomID. The write pump encodes it as the
// "left" frame. EventID and At carry the durable member_left timeline event.
type RoomLeft struct {
	RoomID  string
	User    UserRef
	EventID string
	At      time.Time
}

// ErrorResponse is the typed form of the generic "error" frame: an
// asynchronous, non-authentication error. Code is a parsed shared error code;
// the write pump derives the frame's canonical numeric and status fields from
// it. No connection behavior constructs one yet — it pins the wire shape for
// future async, non-auth errors (see the AsyncAPI contract).
type ErrorResponse struct {
	Code    errcode.Code
	Message string
}

// AppPing is the application-level ping the actor enqueues on an [AppPingTick].
// The write pump encodes it as the "ping" frame; the client answers with a
// "pong" decoded into [AppPongReceived].
type AppPing struct{}

// CloseConnection directs the write pump to flush a WebSocket close frame and
// then report [WritePumpClosed]. Reason is a coarse, log-safe label for why the
// actor is closing. The active behavior queues it (via newClosingBehavior) as
// the last outbound message before tear-down.
type CloseConnection struct {
	Reason string
}

// Internal control messages produced by middleware timers, never sent by the
// client.

// AuthTimeoutExpired is the one-shot timer message from the auth-timeout
// middleware. It fires unconditionally after startup; preAuthBehavior treats it
// as a rejection, while postAuthBehavior ignores it because identity is already
// established.
type AuthTimeoutExpired struct{}

// AppPingTick is the heartbeat middleware's interval tick. On receipt the actor
// enqueues an [AppPing] for the write pump.
type AppPingTick struct{}

// PongTimeoutExpired signals that the heartbeat watchdog elapsed without an
// [AppPongReceived]. The active behavior begins closing the connection.
type PongTimeoutExpired struct{}

// DecodeFailureReason classifies why the decoder could not turn an inbound
// frame into a typed message.
type DecodeFailureReason string

const (
	decodeFailureMalformedJSON DecodeFailureReason = "malformed_json"
	decodeFailureUnknownType   DecodeFailureReason = "unknown_type"
)

// DecodeFailed reports that the read pump received a text frame it could not
// decode — malformed JSON or an unknown "type". The actor logs it and keeps the
// connection open so a client can retry with a well-formed frame.
type DecodeFailed struct {
	Reason DecodeFailureReason
}

// ProtocolViolationReason classifies a structurally valid frame that breaks a
// protocol rule.
type ProtocolViolationReason string

const (
	protocolViolationMissingToken ProtocolViolationReason = "missing_token"
)

// ProtocolViolation reports a well-formed frame that violates a protocol rule,
// such as an "auth" frame whose token is empty. Pre-auth it is treated as an
// auth rejection; post-auth it is logged.
type ProtocolViolation struct {
	Reason ProtocolViolationReason
}

// Transport tear-down signals emitted by the read and write pumps. Each one
// means the connection cannot make further progress, so Receive stops the actor.

// ConnectionClosed reports that the read pump observed a wire-level error or a
// peer-initiated close. Reason is a coarse, log-safe label (see classifyReadErr);
// the underlying error string is deliberately not propagated.
type ConnectionClosed struct {
	Reason string
}

// ReadPumpPanicked reports that the read pump recovered a panic. Cause is the
// recovered value rendered for logging.
type ReadPumpPanicked struct {
	Cause string
}

// WritePumpFailed reports that writing a frame failed. Reason is a coarse label
// for the failure and EventKind names the frame in flight when it failed.
type WritePumpFailed struct {
	Reason    string
	EventKind string
}

// WritePumpClosed reports that the write pump flushed the close frame and exited
// after processing a [CloseConnection]. It is the normal end of an
// actor-initiated protocol close.
type WritePumpClosed struct {
	// CloseFrameErr is non-empty when the close-frame WriteControl returned
	// an error. Empty means the close frame was written successfully (or
	// gorilla wrote it without surfacing an error). Used for diagnostics
	// only — the actor stops regardless of the close-frame outcome.
	CloseFrameErr string
}

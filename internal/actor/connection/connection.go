// Package connection implements the UserConnection actor, a regular
// (non-grain) actor that owns one WebSocket per session. Its lifecycle is
// 1:1 with the WebSocket connection it serves.
//
// Auth happens after WebSocket upgrade as the first text frame, not via
// HTTP headers (see ADR-004). On successful auth the actor registers with
// the user's grain so that Room-grain fan-outs reach the WebSocket. On
// disconnect the actor stops; the User grain learns via a death-watch and
// evicts the entry on its own (ADR-012). There is no Deregister RPC.
package connection

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/gorilla/websocket"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/ids"
	"github.com/oklahomer/blabby/internal/middleware"
)

// actorType is the canonical actor_type value emitted by every log call from
// this package and passed to middleware.ActorLogging. Pinned at one site so
// the envelope contract cannot drift.
const actorType = "UserConnection"

// Event-name constants for every log line this actor emits. The happy-path
// state transition (authenticated) and the lifecycle terminal (closed)
// follow the N3 past-tense action-verb convention. The failure/observation
// events keep their existing names (they describe failure types, not RPC
// outcomes, so the past-tense rule does not apply). Middleware-owned
// envelope events live in internal/middleware as exported constants.
const (
	eventConnectionAuthenticated          = "connection.authenticated"
	eventConnectionClosed                 = "connection.closed"
	eventConnectionWriteMessage           = "connection.write.message"
	eventConnectionAuthRejected           = "connection.auth.rejected"
	eventConnectionAuthContractViolation  = "connection.auth.contract_violation"
	eventConnectionAuthNoUserClient       = "connection.auth.no_user_client"
	eventConnectionProtocolViolation      = "connection.protocol.violation"
	eventConnectionDecodeFailed           = "connection.decode.failed"
	eventConnectionRegisterInlineError    = "connection.register.inline_error"
	eventConnectionRegisterTransportError = "connection.register.transport_error"
	eventConnectionWriteError             = "connection.write.error"
	eventConnectionWriteBackpressure      = "connection.write.backpressure"
	eventConnectionEventUnknown           = "connection.event.unknown"
	eventConnectionCloseFrameError        = "connection.close_frame.error"
	eventConnectionReadPumpPanic          = "connection.read_pump.panic"
)

// errorKind pairs the on-wire numeric code with its on-wire status string.
// Bundling them prevents accidental code/status mismatches at construction
// sites — callers refer to a single named value rather than passing two
// loosely-related primitives.
type errorKind struct {
	Code   int32
	Status string
}

// Error kinds mirror the canonical taxonomy in internal/gateway/errors.go.
// They are duplicated as raw values here to avoid an internal/actor ->
// internal/gateway dependency, which would invert the architectural
// direction.
var (
	errAuthInvalidToken = errorKind{Code: 1001, Status: "AUTH_INVALID_TOKEN"}
	errAuthExpiredToken = errorKind{Code: 1002, Status: "AUTH_EXPIRED_TOKEN"}
	errAuthMissingToken = errorKind{Code: 1003, Status: "AUTH_MISSING_TOKEN"}
	errInternalError    = errorKind{Code: 5001, Status: "INTERNAL_ERROR"}
)

const (
	defaultAuthTimeout = 5 * time.Second
	outboundBufferSize = 64
)

// UserGrainCaller abstracts the cluster RegisterConnection RPC so unit
// tests can inject a fake without spinning up a cluster.
type UserGrainCaller interface {
	RegisterConnection(userID string, req *userpb.RegisterConnectionRequest) (*userpb.RegisterConnectionResponse, error)
}

// UserConnection is the actor that owns the meaning of one WebSocket
// connection. Pumps own transport I/O; this actor owns authentication,
// behavior transitions, and cluster communication decisions.
type UserConnection struct {
	behavior actor.Behavior

	conn       *websocket.Conn
	auth       auth.Authenticator
	userClient UserGrainCaller

	userID ids.UserID

	outbound chan any
	shutdown func()
}

// currentUserID returns the authenticated user id from a *UserConnection
// receiver, or the empty string before auth completes. It is wired into
// the actor-logging middleware via middleware.WithUserIDProvider so log
// lines carry user_id once the actor is post-auth without recurring lookups.
//
// The actor model's single-threaded guarantee makes the direct field read
// safe — no synchronization is needed. Any non-UserConnection actor reaching
// this code is a programming error in the caller; the empty fallback keeps
// the middleware non-fatal in that case.
func currentUserID(ctx actor.ReceiverContext) string {
	uc, ok := ctx.Actor().(*UserConnection)
	if !ok {
		return ""
	}
	return uc.userID.String()
}

// Option configures the UserConnection at construction time. See
// [WithAuthTimeout] and [WithUserGrainCaller].
type Option interface {
	applyConfig(*userConnectionConfig)
}

type optionFunc func(*userConnectionConfig)

func (f optionFunc) applyConfig(c *userConnectionConfig) { f(c) }

// WithAuthTimeout overrides the default first-message auth deadline.
// Used in tests to drive the timeout path quickly.
func WithAuthTimeout(d time.Duration) Option {
	return optionFunc(func(c *userConnectionConfig) { c.authTimeout = d })
}

// WithUserGrainCaller overrides the default cluster-backed grain caller.
// Used in unit tests to assert on RegisterConnection inputs without a
// cluster bootstrap.
func WithUserGrainCaller(uc UserGrainCaller) Option {
	return optionFunc(func(c *userConnectionConfig) { c.userClient = uc })
}

// WithAppHeartbeat enables application-level ping/pong timing for the
// connection. The actor decides what ping and timeout mean; this option only
// configures timer ownership in middleware.
func WithAppHeartbeat(pingInterval, pongTimeout time.Duration) Option {
	return optionFunc(func(c *userConnectionConfig) {
		c.heartbeat = heartbeatConfig{
			pingInterval: pingInterval,
			pongTimeout:  pongTimeout,
		}
	})
}

// NewProps builds an actor.Props for a UserConnection bound to conn. The
// returned Props can be used with rootContext.Spawn to start the actor.
func NewProps(conn *websocket.Conn, a auth.Authenticator, c *cluster.Cluster, opts ...Option) *actor.Props {
	cfg := userConnectionConfig{
		conn:        conn,
		auth:        a,
		authTimeout: defaultAuthTimeout,
	}
	for _, o := range opts {
		o.applyConfig(&cfg)
	}
	if cfg.userClient == nil && c != nil {
		cfg.userClient = newClusterUserGrainCaller(c)
	}

	middlewares := []actor.ReceiverMiddleware{authTimeoutMiddleware(cfg.authTimeout)}
	if cfg.heartbeat.enabled() {
		middlewares = append(middlewares, appHeartbeatMiddleware(cfg.heartbeat))
	}
	middlewares = append(middlewares, middleware.ActorLogging(
		actorType,
		middleware.WithUserIDProvider(currentUserID),
	))

	return actor.PropsFromProducer(
		func() actor.Actor {
			uc := &UserConnection{
				conn:       cfg.conn,
				auth:       cfg.auth,
				userClient: cfg.userClient,
				outbound:   make(chan any, outboundBufferSize),
			}
			uc.behavior = actor.NewBehavior()
			return uc
		},
		actor.WithReceiverMiddleware(middlewares...),
		actor.WithSupervisor(stopOnBackpressureSupervisor),
	)
}

// stopOnBackpressureSupervisor terminates a UserConnection on
// *outboundBackpressureError and escalates anything else to the parent.
//
// UserConnection cannot be usefully restarted: its identity is the
// underlying WebSocket, and a fresh actor instance would have no
// connection to recover. Proto.Actor's default Restart directive is
// therefore wrong for this actor. We map the one expected fatal panic
// (backpressure) to Stop so the actor tears down via the normal Stopping
// flow, and let the gateway decide what to do with anything we did not
// anticipate.
//
// This supervisor only sees panics raised on the actor's message-handling
// path. Panics inside the read/write pump goroutines are recovered locally
// by those goroutines and reported back as *ReadPumpPanicked /
// *WritePumpFailed messages, which Receive's transport tear-down section
// handles directly.
var stopOnBackpressureSupervisor = actor.NewOneForOneStrategy(0, 0,
	func(reason interface{}) actor.Directive {
		if _, ok := reason.(*outboundBackpressureError); ok {
			return actor.StopDirective
		}
		return actor.EscalateDirective
	},
)

// outboundBackpressureError is panicked by sendOutbound when the outbound
// channel is full. stopOnBackpressureSupervisor maps it to
// actor.StopDirective so the actor tears down via the normal Stopping path
// rather than dropping messages silently.
//
// Backpressure here means the write pump cannot keep up with the producer,
// which in our protocol indicates a stuck or hostile peer. Three policies
// were considered:
//   - Block the producer — would freeze the actor on any slow client.
//   - Drop and continue — would corrupt chat ordering and the auth handshake.
//   - Tear down the connection — chosen; the client reconnects fresh.
//
// Restart is meaningless here because the WebSocket lives in this actor
// instance, so Stop is the only viable directive.
type outboundBackpressureError struct{}

func (e *outboundBackpressureError) Error() string { return "outbound channel backpressure" }

type userConnectionConfig struct {
	conn        *websocket.Conn
	auth        auth.Authenticator
	userClient  UserGrainCaller
	authTimeout time.Duration
	heartbeat   heartbeatConfig
}

// Receive handles actor lifecycle messages directly and delegates ordinary
// connection messages to the current behavior.
//
// Proto.Actor calls Receive for every message delivered to this actor's
// mailbox. Simpler actors often put their whole state machine in one type
// switch here. UserConnection uses actor.Behavior instead: Receive keeps the
// lifecycle wiring stable, while uc.behavior.Receive(ctx) dispatches the same
// mailbox message to whichever phase is active now.
//
// On Started, the actor installs the initial preAuthBehavior, builds a
// per-connection context, wires a single shutdown closure that bundles
// cancel() with the outbound-channel and WebSocket closes, and launches
// exactly one read pump and one write pump.
// Those goroutines communicate back through the mailbox using typed internal
// messages and observe ctx cancellation as the shutdown signal. When this
// actor calls ctx.Stop(ctx.Self()), Proto.Actor later delivers Stopping and
// then Stopped. Stopping invokes uc.shutdown(), which cancels the pump
// context, closes the outbound channel, and closes the WebSocket. Stopped is
// informational only.
func (uc *UserConnection) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	// Proto.Actor lifecycle.
	case *actor.Started:
		uc.behavior.Become(uc.preAuthBehavior)

		pumpCtx, cancel := context.WithCancel(context.Background())
		uc.shutdown = func() {
			cancel()
			close(uc.outbound)
			_ = uc.conn.Close()
		}

		root := ctx.ActorSystem().Root
		self := ctx.Self()
		notify := func(message any) {
			root.Send(self, message)
		}
		go runReadPump(pumpCtx, uc.conn, notify)
		go runWritePump(pumpCtx, uc.conn, uc.outbound, notify)
	case *actor.Stopping:
		uc.shutdown()
	case *actor.Stopped:

	// Transport tear-down signals from the pumps. These mean the connection
	// is already gone or cannot make forward progress, so the actor stops
	// itself; resource cleanup happens in the *actor.Stopping branch above.
	// Actor-initiated protocol closes follow a different path: the active
	// behavior installs newClosingBehavior, which queues a CloseConnection
	// on the outbound channel so the write pump can flush the close frame
	// before reporting WritePumpClosed back through this case.
	case *ConnectionClosed:
		uc.logClosed(ctx, msg)
		uc.stop(ctx)
	case *ReadPumpPanicked:
		uc.logReadPumpPanic(ctx, msg)
		uc.stop(ctx)
	case *WritePumpFailed:
		uc.logWriteError(ctx, msg.EventKind)
		uc.stop(ctx)
	case *WritePumpClosed:
		if msg.CloseFrameErr != "" {
			slog.Warn(eventConnectionCloseFrameError,
				"actor_type", actorType,
				"pid", ctx.Self().String(),
				"user_id", uc.userID.String(),
				"reason", msg.CloseFrameErr,
			)
		}
		uc.stop(ctx)

	// Phase-specific messages — auth frames, room events, heartbeat ticks,
	// protocol violations, etc. Receive delegates to the active behavior so
	// each phase's switch can decide what to do; this keeps lifecycle
	// concerns above and phase concerns below cleanly separated.
	default:
		uc.behavior.Receive(ctx)
	}
}

// preAuthBehavior is the behavior used before the WebSocket connection has
// proved a user identity.
//
// A behavior is just a ReceiveFunc installed in actor.Behavior. Calling
// Become swaps future mailbox dispatch to another function without requiring
// an explicit "current phase" field. In this phase, the only client protocol
// message that can make progress is InboundAuth. Decode failures are logged
// and ignored so a client can still retry with a well-formed auth frame before
// the auth timeout. Protocol violations and auth timeout are interpreted as
// auth rejection paths. Pump failures and peer closes stop the actor because
// the connection has no useful state to preserve.
//
// Successful authentication registers this actor with the User grain, emits an
// AuthSucceeded outbound message for the write pump, and switches future
// message handling to postAuthBehavior.
func (uc *UserConnection) preAuthBehavior(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *InboundAuth:
		uid, err := uc.handleAuth(ctx, msg)
		if err != nil {
			var rej *authRejectionError
			errors.As(err, &rej)
			uc.sendOutboundBestEffort(ctx, newAuthFailed(rej.Kind, rej.Message))
			uc.logAuthRejected(ctx, rej.Reason, rej.Kind.Code)
			uc.behavior.Become(uc.newClosingBehavior(ctx, &CloseConnection{Reason: rej.Reason}))
			return
		}
		uc.userID = *uid
		uc.sendOutbound(ctx, &AuthSucceeded{})
		slog.Info(eventConnectionAuthenticated,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", uid.String(),
		)
		uc.behavior.Become(uc.postAuthBehavior)
	case *AuthTimeoutExpired:
		const reason = "auth_timeout"
		uc.sendOutboundBestEffort(ctx, newAuthFailed(errAuthMissingToken, "authentication timeout"))
		uc.logAuthRejected(ctx, reason, errAuthMissingToken.Code)
		uc.behavior.Become(uc.newClosingBehavior(ctx, &CloseConnection{Reason: reason}))
	case *DecodeFailed:
		uc.logDecodeFailure(ctx, msg)
	case *ProtocolViolation:
		reason := string(msg.Reason)
		uc.sendOutboundBestEffort(ctx, newAuthFailed(errAuthMissingToken, "missing authentication token"))
		uc.logAuthRejected(ctx, reason, errAuthMissingToken.Code)
		uc.behavior.Become(uc.newClosingBehavior(ctx, &CloseConnection{Reason: reason}))
	case *AppPingTick:
		uc.sendOutbound(ctx, &AppPing{})
	case *AppPongReceived:
		// The heartbeat middleware owns watchdog reset. A pre-auth pong does
		// not establish identity.
	case *PongTimeoutExpired:
		uc.behavior.Become(uc.newClosingBehavior(ctx, &CloseConnection{Reason: "pong_timeout"}))
	default:
		// Protected operations are rejected until authentication succeeds.
	}
}

// postAuthBehavior is the behavior used after successful authentication and
// User grain registration.
//
// In this phase, the actor accepts server-side business messages from the User
// grain and turns them into typed outbound messages for the write pump. It
// also reacts to application heartbeat control messages. Inbound client frames
// are no longer used for room commands in the current protocol, so protocol
// violations are logged instead of being treated as auth failures.
//
// AuthTimeoutExpired is ignored here because the auth timeout middleware
// schedules a one-shot timer at actor startup. That timer may still arrive
// after the behavior has switched from preAuthBehavior to postAuthBehavior.
func (uc *UserConnection) postAuthBehavior(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *userpb.ForwardMessageRequest:
		uc.forwardMessage(ctx, msg)
	case *userpb.NotifyRoomEventRequest:
		uc.forwardRoomEvent(ctx, msg)
	case *AppPingTick:
		uc.sendOutbound(ctx, &AppPing{})
	case *AppPongReceived:
		// The heartbeat middleware owns watchdog reset. The actor has no
		// connection state to update for a pong.
	case *PongTimeoutExpired:
		uc.behavior.Become(uc.newClosingBehavior(ctx, &CloseConnection{Reason: "pong_timeout"}))
	case *DecodeFailed:
		uc.logDecodeFailure(ctx, msg)
	case *ProtocolViolation:
		slog.Warn(eventConnectionProtocolViolation,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", uc.userID.String(),
			"reason", msg.Reason,
		)
	case *AuthTimeoutExpired:
		// The timer fires unconditionally; after auth it no longer matters.
	default:
	}
}

// newClosingBehavior returns the closing behavior. Constructing it queues cc
// on the outbound channel on a best-effort basis so the close frame is in
// flight as soon as the caller installs the behavior via Become. The
// returned ReceiveFunc gates the non-lifecycle message space — Receive
// handles transport tear-down and Proto.Actor lifecycle directly, so
// closing only needs to drop everything else until *actor.Stopping arrives.
func (uc *UserConnection) newClosingBehavior(ctx actor.Context, cc *CloseConnection) actor.ReceiveFunc {
	uc.sendOutboundBestEffort(ctx, cc)
	return func(ctx actor.Context) {}
}

// authRejectionError carries the AuthFailed payload produced by handleAuth on a
// rejection path. preAuthBehavior converts it into the on-the-wire AuthFailed
// and CloseConnection messages; handleAuth itself stays out of protocol I/O.
type authRejectionError struct {
	Kind    errorKind
	Message string
	Reason  string
}

func (e *authRejectionError) Error() string { return e.Reason }

func newAuthFailed(k errorKind, message string) *AuthFailed {
	return &AuthFailed{Code: k.Code, Status: k.Status, Message: message}
}

// handleAuth runs token validation and User-grain registration. It performs
// no protocol I/O and does not change actor state or behavior. On success it
// returns the resolved UserID and a nil error. On failure it returns a nil
// UserID and an *authRejectionError describing the rejection so the caller can
// build the appropriate AuthFailed and transition.
func (uc *UserConnection) handleAuth(ctx actor.Context, msg *InboundAuth) (*ids.UserID, error) {
	claims, err := uc.auth.ValidateToken(context.Background(), msg.Token.String())
	if err != nil {
		if errors.Is(err, auth.ErrTokenExpired) {
			return nil, &authRejectionError{Kind: errAuthExpiredToken, Message: "token has expired", Reason: "expired"}
		}
		return nil, &authRejectionError{Kind: errAuthInvalidToken, Message: "invalid token", Reason: "invalid"}
	}

	if claims == nil {
		slog.Error(eventConnectionAuthContractViolation,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"reason", "claims_nil",
		)
		return nil, &authRejectionError{Kind: errAuthInvalidToken, Message: "invalid token", Reason: "contract_violation"}
	}
	userID := claims.UserID

	if uc.userClient == nil {
		slog.Error(eventConnectionAuthNoUserClient,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
		)
		return nil, &authRejectionError{Kind: errInternalError, Message: "service unavailable", Reason: "no_user_client"}
	}

	resp, err := uc.userClient.RegisterConnection(userID.String(), &userpb.RegisterConnectionRequest{
		RequesterPid: &userpb.PID{Address: ctx.Self().Address, Id: ctx.Self().Id},
	})
	if err != nil {
		slog.Warn(eventConnectionRegisterTransportError,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", userID.String(),
			"reason", "transport_error",
		)
		return nil, &authRejectionError{Kind: errInternalError, Message: "service unavailable", Reason: "register_transport_error"}
	}
	if respErr := resp.GetError(); respErr != nil {
		// The grain's Code and Status flow through to the client; the
		// grain's Message is logged server-side for ops debuggability but
		// replaced by a fixed in-actor lookup before crossing the wire so
		// grain-supplied prose never leaks to the client.
		slog.Warn(eventConnectionRegisterInlineError,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", userID.String(),
			"code", respErr.GetCode(),
			"status", respErr.GetStatus(),
			"grain_message", respErr.GetMessage(),
			"reason", "register_inline_error",
		)
		return nil, &authRejectionError{
			Kind:    errorKind{Code: respErr.GetCode(), Status: respErr.GetStatus()},
			Message: inlineErrorClientMessage(respErr.GetStatus()),
			Reason:  "register_inline_error",
		}
	}

	return &userID, nil
}

// inlineErrorClientMessages maps a register-inline-error status produced by
// the user grain to the client-facing message that crosses the wire. The
// grain's verbose message is logged server-side via
// connection.register.inline_error and never sent to the client. Add a new
// entry when the grain introduces a new inline-error status; the default
// fallback is intentionally generic.
var inlineErrorClientMessages = map[string]string{
	// INVALID_REQUEST today maps to the empty-PID defense-in-depth path.
	"INVALID_REQUEST": "registration rejected",
}

func inlineErrorClientMessage(status string) string {
	if msg, ok := inlineErrorClientMessages[status]; ok {
		return msg
	}
	return "registration rejected"
}

func (uc *UserConnection) logAuthRejected(ctx actor.Context, reason string, code int32) {
	slog.Warn(eventConnectionAuthRejected,
		"actor_type", actorType,
		"pid", ctx.Self().String(),
		"reason", reason,
		"code", code,
	)
}

func (uc *UserConnection) forwardMessage(ctx actor.Context, req *userpb.ForwardMessageRequest) {
	var timestamp time.Time
	if ts := req.GetTimestamp(); ts != nil {
		timestamp = ts.AsTime()
	}
	uc.sendOutbound(ctx, &ChatDelivered{
		RoomID:    req.GetRoomId(),
		SenderID:  req.GetSenderId(),
		Text:      req.GetText(),
		Timestamp: timestamp,
	})
	slog.Debug(eventConnectionWriteMessage,
		"actor_type", actorType,
		"pid", ctx.Self().String(),
		"user_id", uc.userID.String(),
		"room_id", req.GetRoomId(),
		"sender_id", req.GetSenderId(),
		"text_len", len(req.GetText()),
	)
}

func (uc *UserConnection) forwardRoomEvent(ctx actor.Context, req *userpb.NotifyRoomEventRequest) {
	var out any
	switch req.GetEventType() {
	case userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED:
		out = &RoomJoined{RoomID: req.GetRoomId(), UserID: req.GetUserId()}
	case userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT:
		out = &RoomLeft{RoomID: req.GetRoomId(), UserID: req.GetUserId()}
	default:
		slog.Warn(eventConnectionEventUnknown,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", uc.userID.String(),
			"event_type", req.GetEventType().String(),
		)
		return
	}
	uc.sendOutbound(ctx, out)
}

// sendOutbound enqueues msg on the outbound channel for the write pump.
// It returns normally on success and panics with *outboundBackpressureError
// when the channel is full. The actor's supervisor
// (stopOnBackpressureSupervisor) catches that panic and stops the actor,
// so callers do not need to handle the failure path themselves — control
// flow simply unwinds out of the current message handler.
func (uc *UserConnection) sendOutbound(ctx actor.Context, msg any) {
	select {
	case uc.outbound <- msg:
	default:
		slog.Warn(eventConnectionWriteBackpressure,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", uc.userID.String(),
		)
		panic(&outboundBackpressureError{})
	}
}

// sendOutboundBestEffort enqueues msg if there is room and otherwise drops it.
// Used for the close frame, which is advisory: if the channel is full the peer
// is likely stale or already gone, and the actor will tear down regardless.
func (uc *UserConnection) sendOutboundBestEffort(ctx actor.Context, msg any) {
	select {
	case uc.outbound <- msg:
	default:
		slog.Warn(eventConnectionWriteBackpressure,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", uc.userID.String(),
			"dropped_kind", typeName(msg),
		)
	}
}

// stop requests actor termination. Resource cleanup is deliberately centralized
// in the *actor.Stopping branch of Receive, because Proto.Actor sends Stopping
// for self-stop, parent/supervisor stop, and external Stop/Poison paths.
func (uc *UserConnection) stop(ctx actor.Context) {
	ctx.Stop(ctx.Self())
}

func (uc *UserConnection) logDecodeFailure(ctx actor.Context, msg *DecodeFailed) {
	slog.Warn(eventConnectionDecodeFailed,
		"actor_type", actorType,
		"pid", ctx.Self().String(),
		"user_id", uc.userID.String(),
		"reason", msg.Reason,
	)
}

func (uc *UserConnection) logReadPumpPanic(ctx actor.Context, _ *ReadPumpPanicked) {
	slog.Error(eventConnectionReadPumpPanic,
		"actor_type", actorType,
		"pid", ctx.Self().String(),
		"user_id", uc.userID.String(),
		"reason", "read_pump_panicked",
	)
}

func (uc *UserConnection) logClosed(ctx actor.Context, msg *ConnectionClosed) {
	args := []any{
		"actor_type", actorType,
		"pid", ctx.Self().String(),
		"msg_type", typeName(msg),
		"reason", msg.Reason,
	}
	if uid := uc.userID.String(); uid != "" {
		args = append(args, "user_id", uid)
	}
	slog.Info(eventConnectionClosed, args...)
}

func (uc *UserConnection) logWriteError(ctx actor.Context, eventKind string) {
	slog.Warn(eventConnectionWriteError,
		"actor_type", actorType,
		"pid", ctx.Self().String(),
		"user_id", uc.userID.String(),
		"reason", "write_error",
		"event_kind", eventKind,
	)
}

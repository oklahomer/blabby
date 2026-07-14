// Package connection implements the UserConnection actor, a regular
// (non-grain) actor that owns one WebSocket per session. Its lifecycle is
// 1:1 with the WebSocket connection it serves.
//
// Auth happens after WebSocket upgrade as the first text frame, not via
// HTTP headers (see ADR-003). On successful auth the actor registers with
// the user's grain so that Room-grain fan-outs reach the WebSocket. The
// watch between the two is bidirectional (ADR-006). On disconnect the actor
// stops; the User grain learns via a death-watch and evicts the entry on its
// own (ADR-012) — there is no Deregister RPC. In return the actor watches
// the grain activation it registered with and re-registers when that
// activation dies, so deliveries survive a grain relocation without a
// client reconnect.
package connection

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/scheduler"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/id"
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
	eventConnectionRegisterNoGrainPid     = "connection.register.no_grain_pid"
	eventConnectionReregisterSucceeded    = "connection.reregister.succeeded"
	eventConnectionReregisterFailed       = "connection.reregister.failed"
	eventConnectionWriteError             = "connection.write.error"
	eventConnectionWriteBackpressure      = "connection.write.backpressure"
	eventConnectionEventUnknown           = "connection.event.unknown"
	eventConnectionRoomRefInvalid         = "connection.room_ref.invalid"
	eventConnectionUserRefInvalid         = "connection.user_ref.invalid"
	eventConnectionCloseInitiated         = "connection.close_initiated"
	eventConnectionCloseFrameError        = "connection.close_frame.error"
	eventConnectionReadPumpPanic          = "connection.read_pump.panic"
	eventConnectionSupervision            = "connection.supervision"
)

const (
	defaultAuthTimeout = 5 * time.Second
	outboundBufferSize = 64

	// Re-registration retry policy (ADR-006). A re-register addresses the
	// grain by identity, which itself reactivates it, so the first attempt
	// usually succeeds; a short fixed delay beats exponential backoff until
	// measurement says otherwise.
	reregisterMaxAttempts       = 3
	defaultReregisterRetryDelay = time.Second
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

	userID id.UserID

	// grainPID is the User grain activation this connection registered with
	// and watches, so the grain's death triggers a re-register (ADR-006).
	// Nil when the grain did not report one (version skew); then self-healing
	// degrades to client-driven recovery for this session.
	grainPID              *actor.PID
	reregisterAttempts    int
	reregisterRetryDelay  time.Duration
	reregisterRetryCancel scheduler.CancelFunc

	outbound  chan any
	shutdown  func()
	heartbeat *heartbeatTimers
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
	if !ok || uc.userID.IsZero() {
		return ""
	}
	return uc.userID.String()
}

// Option configures the UserConnection at construction time. See
// [WithAuthTimeout], [WithUserGrainCaller], and [WithAppHeartbeat].
type Option func(*userConnectionConfig)

// WithAuthTimeout overrides the default first-message auth deadline.
// Used in tests to drive the timeout path quickly.
func WithAuthTimeout(d time.Duration) Option {
	return func(c *userConnectionConfig) { c.authTimeout = d }
}

// WithUserGrainCaller overrides the default cluster-backed grain caller.
// Used in unit tests to assert on RegisterConnection inputs without a
// cluster bootstrap.
func WithUserGrainCaller(uc UserGrainCaller) Option {
	return func(c *userConnectionConfig) { c.userClient = uc }
}

// WithReregisterRetryDelay overrides the default delay between re-register
// attempts after the watched grain activation dies. Used in tests to drive
// the retry-exhaustion path quickly.
func WithReregisterRetryDelay(d time.Duration) Option {
	return func(c *userConnectionConfig) { c.reregisterRetryDelay = d }
}

// WithAppHeartbeat enables application-level ping/pong timing for the
// connection with the given cadence; the production gateway passes its
// default cadence for every /ws spawn. The option only sets the timing —
// the actor owns the timers themselves, one [heartbeatTimers] per spawn,
// driven from Receive, and decides what ping and timeout mean.
func WithAppHeartbeat(cadence HeartbeatCadence) Option {
	return func(c *userConnectionConfig) { c.heartbeat = cadence }
}

// NewProps builds an actor.Props for a UserConnection bound to conn. The
// returned Props can be used with rootContext.Spawn to start the actor.
func NewProps(conn *websocket.Conn, a auth.Authenticator, c *cluster.Cluster, opts ...Option) *actor.Props {
	cfg := userConnectionConfig{
		conn:                 conn,
		auth:                 a,
		authTimeout:          defaultAuthTimeout,
		reregisterRetryDelay: defaultReregisterRetryDelay,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.userClient == nil && c != nil {
		cfg.userClient = newClusterUserGrainCaller(c)
	}

	middlewares := []actor.ReceiverMiddleware{
		authTimeoutMiddleware(cfg.authTimeout),
		middleware.ActorLogging(
			actorType,
			middleware.WithUserIDProvider(currentUserID),
		),
	}

	return actor.PropsFromProducer(
		func() actor.Actor {
			uc := &UserConnection{
				conn:                 cfg.conn,
				auth:                 cfg.auth,
				userClient:           cfg.userClient,
				reregisterRetryDelay: cfg.reregisterRetryDelay,
				outbound:             make(chan any, outboundBufferSize),
				heartbeat:            newHeartbeatTimers(cfg.heartbeat),
			}
			uc.behavior = actor.NewBehavior()
			return uc
		},
		actor.WithReceiverMiddleware(middlewares...),
		actor.WithGuardian(connectionSupervisor),
	)
}

type userConnectionConfig struct {
	conn                 *websocket.Conn
	auth                 auth.Authenticator
	userClient           UserGrainCaller
	authTimeout          time.Duration
	reregisterRetryDelay time.Duration
	heartbeat            HeartbeatCadence
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
		uc.heartbeat.start(ctx)
	case *actor.Stopping:
		uc.heartbeat.stop()
		uc.cancelReregisterRetry()
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
			slog.Warn(eventConnectionCloseFrameError, uc.logAttrs(ctx, "reason", msg.CloseFrameErr)...)
		}
		uc.stop(ctx)

	// Application heartbeat bookkeeping. The timers are actor-owned,
	// phase-independent state, so Receive maintains them for every phase
	// and then lets the active behavior decide what the message means
	// (emit a ping frame, begin closing on timeout, ignore while closing).
	case *AppPingTick:
		uc.behavior.Receive(ctx)
		uc.heartbeat.ensureWatchdog(ctx)
	case *AppPongReceived:
		uc.behavior.Receive(ctx)
		uc.heartbeat.resetWatchdog(ctx)
	case *PongTimeoutExpired:
		uc.heartbeat.cancelWatchdog()
		uc.behavior.Receive(ctx)

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
// Successful authentication registers this actor with the User grain, arms a
// watch on the grain's activation PID so its death triggers a re-register
// (ADR-006), emits an AuthSucceeded outbound message for the write pump, and
// switches future message handling to postAuthBehavior.
func (uc *UserConnection) preAuthBehavior(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *InboundAuth:
		uid, grainPID, rej := uc.handleAuth(ctx, msg)
		if rej != nil {
			uc.sendOutboundBestEffort(ctx, &AuthFailed{Code: rej.Code, Message: rej.Message})
			uc.logAuthRejected(ctx, rej.Reason, rej.Code)
			uc.behavior.Become(uc.newClosingBehavior(ctx, &CloseConnection{Reason: rej.Reason}))
			return
		}
		uc.userID = uid
		uc.grainPID = grainPID
		if grainPID != nil {
			ctx.Watch(grainPID)
		}
		uc.sendOutbound(ctx, &AuthSucceeded{})
		slog.Info(eventConnectionAuthenticated,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", uid.String(),
		)
		uc.behavior.Become(uc.postAuthBehavior)
	case *AuthTimeoutExpired:
		const reason = "auth_timeout"
		uc.sendOutboundBestEffort(ctx, &AuthFailed{Code: errcode.AuthMissingToken, Message: "authentication timeout"})
		uc.logAuthRejected(ctx, reason, errcode.AuthMissingToken)
		uc.behavior.Become(uc.newClosingBehavior(ctx, &CloseConnection{Reason: reason}))
	case *DecodeFailed:
		uc.logDecodeFailure(ctx, msg)
	case *ProtocolViolation:
		reason := string(msg.Reason)
		uc.sendOutboundBestEffort(ctx, &AuthFailed{Code: errcode.AuthMissingToken, Message: "missing authentication token"})
		uc.logAuthRejected(ctx, reason, errcode.AuthMissingToken)
		uc.behavior.Become(uc.newClosingBehavior(ctx, &CloseConnection{Reason: reason}))
	case *AppPingTick:
		uc.sendOutbound(ctx, &AppPing{})
	case *AppPongReceived:
		// Receive's heartbeat bookkeeping owns the watchdog reset. A pre-auth
		// pong does not establish identity.
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
// This phase also owns the reverse half of the bidirectional watch (ADR-006):
// Terminated for the watched grain activation, or a ReregisterRetry tick after
// a failed attempt, drives a re-register so a fresh activation learns about
// this connection without waiting for the client to reconnect. Handling these
// here rather than in Receive makes phase-correctness free — pre-auth has no
// watch, and the closing behavior drops stray ticks.
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
	case *actor.Terminated:
		// Death of the watched User grain activation. Anything else — e.g. a
		// late notification for an already-replaced activation — is ignored.
		// samePID compares by value: Who is never the stored pointer.
		if !samePID(msg.Who, uc.grainPID) {
			return
		}
		// A pending retry means a repair cycle is already in flight for this
		// same dead activation (grainPID only rotates on success). A duplicate
		// death signal — a real Terminated plus an endpoint-terminated
		// translation, say — must not start a second chain and orphan the
		// pending timer's cancel handle.
		if uc.reregisterRetryCancel != nil {
			return
		}
		if uc.attemptReregister(ctx) {
			uc.behavior.Become(uc.newClosingBehavior(ctx, &CloseConnection{Reason: "reregister_failed"}))
		}
	case *ReregisterRetry:
		uc.reregisterRetryCancel = nil // the one-shot has fired; the handle is spent
		if uc.attemptReregister(ctx) {
			uc.behavior.Become(uc.newClosingBehavior(ctx, &CloseConnection{Reason: "reregister_failed"}))
		}
	case *AppPingTick:
		uc.sendOutbound(ctx, &AppPing{})
	case *AppPongReceived:
		// Receive's heartbeat bookkeeping owns the watchdog reset. The
		// behavior has no connection state to update for a pong.
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

// newClosingBehavior returns the closing behavior. Constructing it records
// the close reason and queues cc on the outbound channel so the close frame
// is in flight as soon as the caller installs the behavior via Become. The
// enqueue is the supervised send on purpose: if the channel is full, the
// backpressure panic reaches the guardian and stops the actor — a close that
// cannot even be queued must still tear the connection down, not leave the
// actor idling forever in a behavior that never terminates. The log line is
// emitted first so the intent is recorded even on that panic path. The
// returned ReceiveFunc gates the non-lifecycle message space — Receive
// handles transport tear-down and Proto.Actor lifecycle directly, so
// closing only needs to drop everything else until *actor.Stopping arrives.
func (uc *UserConnection) newClosingBehavior(ctx actor.Context, cc *CloseConnection) actor.ReceiveFunc {
	slog.Info(eventConnectionCloseInitiated, uc.logAttrs(ctx, "reason", cc.Reason)...)
	uc.sendOutbound(ctx, cc)
	return func(ctx actor.Context) {}
}

// logAttrs assembles the attribute prefix shared by this actor's log lines —
// actor_type and pid, plus user_id once authentication has bound an identity —
// followed by extra. Log sites that can fire pre-auth build their attributes
// here so the zero id's "0" rendering never appears as a user_id; post-auth-only
// sites log a real id by construction and keep their inline attribute lists.
func (uc *UserConnection) logAttrs(ctx actor.Context, extra ...any) []any {
	attrs := []any{
		"actor_type", actorType,
		"pid", ctx.Self().String(),
	}
	if !uc.userID.IsZero() {
		attrs = append(attrs, "user_id", uc.userID.String())
	}
	return append(attrs, extra...)
}

// authRejection carries the AuthFailed payload produced by handleAuth on a
// rejection path. preAuthBehavior converts it into the on-the-wire AuthFailed
// and CloseConnection messages; handleAuth itself stays out of protocol I/O.
type authRejection struct {
	Code    errcode.Code
	Message string
	Reason  string
}

// handleAuth runs token validation and User-grain registration. It performs
// no protocol I/O and does not change actor state or behavior. On success it
// returns the resolved UserID, the responding grain activation's PID for the
// caller to watch (nil when the grain did not report one — version skew; the
// caller proceeds without self-healing rather than failing auth), and a nil
// rejection. On failure it returns the zero UserID, a nil PID, and an
// *authRejection describing the rejection so the caller can build the
// appropriate AuthFailed and pick the next behavior.
func (uc *UserConnection) handleAuth(ctx actor.Context, msg *InboundAuth) (id.UserID, *actor.PID, *authRejection) {
	claims, err := uc.auth.ValidateToken(context.Background(), msg.Token.String())
	if err != nil {
		if errors.Is(err, auth.ErrTokenExpired) {
			return id.UserID{}, nil, &authRejection{Code: errcode.AuthExpiredToken, Message: "token has expired", Reason: "expired"}
		}
		if errors.Is(err, auth.ErrIdentityUnavailable) {
			return id.UserID{}, nil, &authRejection{Code: errcode.ServiceUnavailable, Message: "authentication temporarily unavailable", Reason: "unavailable"}
		}
		return id.UserID{}, nil, &authRejection{Code: errcode.AuthInvalidToken, Message: "invalid token", Reason: "invalid"}
	}

	if claims == nil {
		slog.Error(eventConnectionAuthContractViolation,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"reason", "claims_nil",
		)
		return id.UserID{}, nil, &authRejection{Code: errcode.AuthInvalidToken, Message: "invalid token", Reason: "contract_violation"}
	}
	userID := claims.UserID

	if uc.userClient == nil {
		slog.Error(eventConnectionAuthNoUserClient,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
		)
		return id.UserID{}, nil, &authRejection{Code: errcode.InternalError, Message: "service unavailable", Reason: "no_user_client"}
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
		return id.UserID{}, nil, &authRejection{Code: errcode.InternalError, Message: "service unavailable", Reason: "register_transport_error"}
	}
	if respErr := resp.GetError(); respErr != nil {
		code, parseErr := errcode.Parse(respErr.GetCode(), respErr.GetStatus())
		slog.Warn(eventConnectionRegisterInlineError,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", userID.String(),
			"code", respErr.GetCode(),
			"status", respErr.GetStatus(),
			"grain_message", respErr.GetMessage(),
			"taxonomy_error", parseErr,
			"reason", "register_inline_error",
		)
		if parseErr != nil {
			return id.UserID{}, nil, &authRejection{
				Code:    errcode.InternalError,
				Message: "service unavailable",
				Reason:  "register_inline_error_invalid_taxonomy",
			}
		}
		return id.UserID{}, nil, &authRejection{
			Code:    code,
			Message: inlineErrorClientMessage(code),
			Reason:  "register_inline_error",
		}
	}

	grainPID := grainPIDFromResponse(resp)
	if grainPID == nil {
		slog.Warn(eventConnectionRegisterNoGrainPid,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", userID.String(),
			"path", "auth",
		)
	}
	return userID, grainPID, nil
}

// inlineErrorClientMessages maps a parsed register-inline-error code produced
// by the user grain to the client-facing message that crosses the wire. The
// grain's verbose message is logged server-side via
// connection.register.inline_error and never sent to the client. Add a new
// entry when the grain introduces a new inline-error code; the default
// fallback is intentionally generic.
var inlineErrorClientMessages = map[errcode.Code]string{
	// INVALID_REQUEST today maps to the empty-PID defense-in-depth path.
	errcode.InvalidRequest: "registration rejected",
}

func inlineErrorClientMessage(code errcode.Code) string {
	if msg, ok := inlineErrorClientMessages[code]; ok {
		return msg
	}
	return "registration rejected"
}

func (uc *UserConnection) logAuthRejected(ctx actor.Context, reason string, code errcode.Code) {
	slog.Warn(eventConnectionAuthRejected,
		"actor_type", actorType,
		"pid", ctx.Self().String(),
		"reason", reason,
		"code", code.Int32(),
	)
}

// roomCodeFromRef renders the client-facing R… code from the room ref carried by
// a fan-out message. It returns ok=false when the ref is missing or its public
// code is unparseable; the caller drops the frame rather than send a malformed or
// internal-leaking code onto the wire.
func (uc *UserConnection) roomCodeFromRef(ctx actor.Context, room *commonpb.RoomRef) (string, bool) {
	if room == nil {
		slog.Error(eventConnectionRoomRefInvalid,
			"actor_type", actorType, "pid", ctx.Self().String(),
			"user_id", uc.userID.String(),
			"reason", "missing_room_ref")
		return "", false
	}
	code, err := id.ParsePublicCode(room.GetPublicCode())
	if err != nil {
		slog.Error(eventConnectionRoomRefInvalid,
			"actor_type", actorType, "pid", ctx.Self().String(),
			"user_id", uc.userID.String(), "room_id", room.GetRoomId(),
			"reason", "unparseable_public_code", "error", err)
		return "", false
	}
	return code.FormatRoom(), true
}

// userCodeFromRef renders the client-facing U… code from the user ref carried
// by a fan-out message, mirroring roomCodeFromRef. It returns ok=false when the
// ref is missing or its public code is unparseable; the caller drops the frame
// rather than send a malformed or internal-id-leaking value onto the wire.
func (uc *UserConnection) userCodeFromRef(ctx actor.Context, user *commonpb.UserRef) (string, bool) {
	if user == nil {
		slog.Error(eventConnectionUserRefInvalid,
			"actor_type", actorType, "pid", ctx.Self().String(),
			"user_id", uc.userID.String(), "reason", "missing_user_ref")
		return "", false
	}
	code, err := id.ParsePublicCode(user.GetPublicCode())
	if err != nil {
		slog.Error(eventConnectionUserRefInvalid,
			"actor_type", actorType, "pid", ctx.Self().String(),
			"user_id", uc.userID.String(),
			"reason", "unparseable_public_code", "error", err)
		return "", false
	}
	return code.FormatUser(), true
}

func (uc *UserConnection) forwardMessage(ctx actor.Context, req *userpb.ForwardMessageRequest) {
	roomCode, ok := uc.roomCodeFromRef(ctx, req.GetRoom())
	if !ok {
		return
	}
	senderCode, ok := uc.userCodeFromRef(ctx, req.GetSender())
	if !ok {
		return
	}
	uc.sendOutbound(ctx, &ChatDelivered{
		RoomID:    roomCode,
		Sender:    UserRef{ID: senderCode, Name: req.GetSender().GetName()},
		Text:      req.GetText(),
		Timestamp: protoTime(req.GetTimestamp()),
		EventID:   req.GetEventId(),
	})
	slog.Debug(eventConnectionWriteMessage,
		"actor_type", actorType,
		"pid", ctx.Self().String(),
		"user_id", uc.userID.String(),
		"room_id", roomCode,
		"sender_code", senderCode,
		"event_id", req.GetEventId(),
		"text_len", len(req.GetText()),
	)
}

func (uc *UserConnection) forwardRoomEvent(ctx actor.Context, req *userpb.NotifyRoomEventRequest) {
	// Classify the event first, so an unknown type is dropped without spending a
	// room-code lookup it would never use.
	eventType := req.GetEventType()
	if eventType != userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED &&
		eventType != userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT {
		slog.Warn(eventConnectionEventUnknown,
			"actor_type", actorType,
			"pid", ctx.Self().String(),
			"user_id", uc.userID.String(),
			"event_type", eventType.String(),
		)
		return
	}

	roomCode, ok := uc.roomCodeFromRef(ctx, req.GetRoom())
	if !ok {
		return
	}
	userCode, ok := uc.userCodeFromRef(ctx, req.GetUser())
	if !ok {
		return
	}
	user := UserRef{ID: userCode, Name: req.GetUser().GetName()}
	at := protoTime(req.GetTimestamp())
	switch eventType {
	case userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED:
		uc.sendOutbound(ctx, &RoomJoined{RoomID: roomCode, User: user, EventID: req.GetEventId(), At: at})
	case userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT:
		uc.sendOutbound(ctx, &RoomLeft{RoomID: roomCode, User: user, EventID: req.GetEventId(), At: at})
	}
}

func protoTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// sendOutbound enqueues msg on the outbound channel for the write pump.
// It returns normally on success and panics with *outboundBackpressureError
// when the channel is full. The actor's guardian supervisor (see
// newConnectionSupervisor / classifyConnectionFailure) catches that panic and
// stops the actor, so callers do not need to handle the failure path
// themselves — control flow simply unwinds out of the current message handler.
func (uc *UserConnection) sendOutbound(ctx actor.Context, msg any) {
	select {
	case uc.outbound <- msg:
	default:
		slog.Warn(eventConnectionWriteBackpressure, uc.logAttrs(ctx)...)
		panic(&outboundBackpressureError{})
	}
}

// sendOutboundBestEffort enqueues msg if there is room and otherwise drops it.
// Used for the pre-auth AuthFailed courtesy frames: each send is immediately
// followed by the closing transition, so dropping one only costs the peer a
// nicer error message — the close itself still happens.
func (uc *UserConnection) sendOutboundBestEffort(ctx actor.Context, msg any) {
	select {
	case uc.outbound <- msg:
	default:
		slog.Warn(eventConnectionWriteBackpressure, uc.logAttrs(ctx, "dropped_kind", typeName(msg))...)
	}
}

// stop requests actor termination. Resource cleanup is deliberately centralized
// in the *actor.Stopping branch of Receive, because Proto.Actor sends Stopping
// for self-stop, parent/supervisor stop, and external Stop/Poison paths.
func (uc *UserConnection) stop(ctx actor.Context) {
	ctx.Stop(ctx.Self())
}

func (uc *UserConnection) logDecodeFailure(ctx actor.Context, msg *DecodeFailed) {
	slog.Warn(eventConnectionDecodeFailed, uc.logAttrs(ctx, "reason", msg.Reason)...)
}

func (uc *UserConnection) logReadPumpPanic(ctx actor.Context, _ *ReadPumpPanicked) {
	slog.Error(eventConnectionReadPumpPanic, uc.logAttrs(ctx, "reason", "read_pump_panicked")...)
}

func (uc *UserConnection) logClosed(ctx actor.Context, msg *ConnectionClosed) {
	slog.Info(eventConnectionClosed, uc.logAttrs(ctx,
		"msg_type", typeName(msg),
		"reason", msg.Reason,
	)...)
}

func (uc *UserConnection) logWriteError(ctx actor.Context, eventKind string) {
	slog.Warn(eventConnectionWriteError, uc.logAttrs(ctx,
		"reason", "write_error",
		"event_kind", eventKind,
	)...)
}

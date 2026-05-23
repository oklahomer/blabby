// Package middleware provides protoactor receiver middlewares that emit
// structured slog entries for grain and actor message dispatch.
//
// # MIDDLEWARE ORDER
//
// The architecture's middleware-ordering rule is: persistence (when added) →
// rate-limit (when added) → auth-timeout / heartbeat → logging. Logging is
// always installed last so it records the message exactly as the grain or
// actor will receive it, including any synthetic messages produced by
// upstream middleware (e.g., *AuthTimeoutExpired, *AppPingTick).
//
// # MESSAGE TYPE RESOLUTION
//
// The middleware uses fmt.Sprintf("%T", msg) with the leading '*' trimmed —
// deterministic, fast, no descriptor lookup. For *cluster.GrainRequest the
// resolved name is the wire envelope ("cluster.GrainRequest"); the
// human-readable RPC name lives on the downstream domain follow-up line
// (e.g., user.join.routed for the successful path, user.join_room.rejected
// for validation failures, grain.transport.error for transport failures).
// Joining log lines on grain_id reconstructs the trail. The type-name
// function never serializes message fields, so the middleware cannot leak
// credentials, tokens, or message bodies.
//
// # EMISSION ORDER
//
// Lifecycle events (*actor.Started, *actor.Stopping) log BEFORE next(ctx,
// env) so a slow Stopping handler is still attributed to the right grain
// in the log stream. Ordinary messages log AFTER next so a panic during
// dispatch is visible by line absence — the supervisor's panic recovery
// surfaces it without the middleware double-logging.
package middleware

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/asynkron/protoactor-go/actor"
)

// Middleware-emitted envelope + lifecycle event names — emitted by this
// package's GrainLogging and ActorLogging middlewares themselves.
const (
	EventGrainActivated      = "grain.activated"
	EventGrainPassivated     = "grain.passivated"
	EventGrainLifecycle      = "grain.lifecycle"
	EventGrainMsg            = "grain.msg"
	EventConnectionLifecycle = "connection.lifecycle"
	EventConnectionMsg       = "connection.msg"
)

// Cross-cutting event names emitted by grain handlers in both the user
// and room packages. Centralized here so a typo in one package cannot
// drift from the other. Package-local handler events (user.*, room.*,
// connection.*) live in their respective packages but follow the same
// past-tense action-verb convention (e.g., user.room.joined for the
// happy path, user.room.join_rejected for validation refusals).
const (
	EventGrainFanout               = "grain.fanout"
	EventGrainFanoutError          = "grain.fanout.error"
	EventGrainUnhandled            = "grain.unhandled"
	EventGrainTransportError       = "grain.transport.error"
	EventGrainConnectionTerminated = "grain.connection.terminated"
)

// LoggingOption configures GrainLogging and ActorLogging. Use [WithLogger]
// to redirect output (tests typically capture a *bytes.Buffer) and
// [WithUserIDProvider] to resolve the authenticated user id lazily for
// non-grain actors.
type LoggingOption func(*loggingConfig)

type loggingConfig struct {
	logger     *slog.Logger
	userIDFunc func(actor.ReceiverContext) string
}

func newConfig(opts []LoggingOption) loggingConfig {
	cfg := loggingConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// logger returns the configured *slog.Logger, falling back to
// slog.Default() if no override was supplied.
func (c loggingConfig) effectiveLogger() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// WithLogger overrides the *slog.Logger the middleware writes to. The
// default is slog.Default(). Tests use this to capture output into a
// *bytes.Buffer via slog.NewJSONHandler(&buf, nil).
//
// Passing nil is equivalent to omitting the option entirely — the middleware
// falls back to slog.Default(). This matches the established Go idiom (e.g.
// http.Server{Handler: nil} → DefaultServeMux) and lets callers wire an
// optionally-configured logger without nil-guarding at the call site.
func WithLogger(l *slog.Logger) LoggingOption {
	return func(c *loggingConfig) { c.logger = l }
}

// WithUserIDProvider lets ActorLogging callers resolve the current user id
// at log time. The provider is called once per dispatched message; an
// empty return omits the user_id attribute entirely (no `"user_id":""`
// noise in pre-auth log lines).
func WithUserIDProvider(f func(ctx actor.ReceiverContext) string) LoggingOption {
	return func(c *loggingConfig) { c.userIDFunc = f }
}

// GrainLogging returns an actor.ReceiverMiddleware that emits one
// grain.activated / grain.passivated / grain.msg JSON line per dispatched
// message. Envelope attributes are grain_type (the kind name passed in),
// grain_id (ctx.Self().Id), and msg_type (resolved as described in the
// package's MESSAGE TYPE RESOLUTION block). The middleware does not
// extract domain attributes — handler-side logs own user_id, room_id,
// sender_id, target_count, etc.
//
// grainType matches the value passed to cluster.NewKind (e.g.,
// "UserGrain", "RoomGrain"). The middleware operates at the
// actor.ReceiverContext level — it does not introspect
// cluster.GrainContext — so callers wire one instance per kind.
func GrainLogging(grainType string, opts ...LoggingOption) actor.ReceiverMiddleware {
	cfg := newConfig(opts)
	return func(next actor.ReceiverFunc) actor.ReceiverFunc {
		return func(ctx actor.ReceiverContext, env *actor.MessageEnvelope) {
			emitGrain(cfg.effectiveLogger(), grainType, ctx, env, next)
		}
	}
}

// ActorLogging returns an actor.ReceiverMiddleware suitable for non-grain
// actors (today: UserConnection). The envelope uses actor_type +
// actor_path instead of grain_type + grain_id. When WithUserIDProvider is
// supplied and returns a non-empty value, user_id is appended to every
// dispatched-message line.
func ActorLogging(actorType string, opts ...LoggingOption) actor.ReceiverMiddleware {
	cfg := newConfig(opts)
	return func(next actor.ReceiverFunc) actor.ReceiverFunc {
		return func(ctx actor.ReceiverContext, env *actor.MessageEnvelope) {
			emitActor(cfg, actorType, ctx, env, next)
		}
	}
}

// emitGrain dispatches a grain message and emits the canonical log line.
// Lifecycle events log before next so they are still attributed to the
// grain even when the handler is slow or panics; ordinary messages log
// after next so panics surface naturally (no log line = panic in dispatch).
func emitGrain(logger *slog.Logger, grainType string, ctx actor.ReceiverContext, env *actor.MessageEnvelope, next actor.ReceiverFunc) {
	switch env.Message.(type) {
	case *actor.Started:
		logger.Info(EventGrainActivated,
			"grain_type", grainType,
			"grain_id", grainID(ctx),
			"msg_type", "actor.Started",
		)
		next(ctx, env)
	case *actor.Stopping:
		logger.Info(EventGrainPassivated,
			"grain_type", grainType,
			"grain_id", grainID(ctx),
			"msg_type", "actor.Stopping",
		)
		next(ctx, env)
	case *actor.Restarting, *actor.Stopped:
		logger.Info(EventGrainLifecycle,
			"grain_type", grainType,
			"grain_id", grainID(ctx),
			"msg_type", typeName(env.Message),
		)
		next(ctx, env)
	default:
		next(ctx, env)
		logGrainMsg(logger, grainType, ctx, env.Message)
	}
}

func logGrainMsg(logger *slog.Logger, grainType string, ctx actor.ReceiverContext, msg any) {
	logger.Info(EventGrainMsg,
		"grain_type", grainType,
		"grain_id", grainID(ctx),
		"msg_type", typeName(msg),
	)
}

// emitActor mirrors emitGrain for non-grain actors. The user_id attribute
// is appended only when a provider was supplied AND it returns a
// non-empty string — pre-auth dispatches stay free of `"user_id":""`.
func emitActor(cfg loggingConfig, actorType string, ctx actor.ReceiverContext, env *actor.MessageEnvelope, next actor.ReceiverFunc) {
	logger := cfg.effectiveLogger()
	switch env.Message.(type) {
	case *actor.Started, *actor.Stopping, *actor.Restarting, *actor.Stopped:
		logger.Info(EventConnectionLifecycle,
			"actor_type", actorType,
			"actor_path", actorPath(ctx),
			"msg_type", typeName(env.Message),
		)
		next(ctx, env)
	default:
		next(ctx, env)
		logActorMsg(cfg, actorType, ctx, env.Message)
	}
}

func logActorMsg(cfg loggingConfig, actorType string, ctx actor.ReceiverContext, msg any) {
	attrs := []any{
		"actor_type", actorType,
		"actor_path", actorPath(ctx),
		"msg_type", typeName(msg),
	}
	if cfg.userIDFunc != nil {
		if uid := cfg.userIDFunc(ctx); uid != "" {
			attrs = append(attrs, "user_id", uid)
		}
	}
	cfg.effectiveLogger().Info(EventConnectionMsg, attrs...)
}

// grainID extracts the cluster identity from the actor PID.
//
// protoactor-go's partition lookup activates grains under a PID whose Id
// is "partition-activator/<identity>$<unique>". The handler-side logs
// emitted by each grain use ctx.Identity() — the clean "<identity>"
// portion. To keep the middleware envelope and handler-side logs joinable
// on grain_id, the middleware reverses the partition-activator naming.
//
// Assumption: the logical identity does not itself contain '$'. The
// partition-activator's '$' separates the suffix; LastIndex on '$' would
// truncate a legitimate "team$alpha" identity to "team". The structural
// rules in internal/id reject '/', control characters, and Unicode
// whitespace but do NOT reject '$'; today's callers happen to emit
// '$'-free identities (UUID-shaped user_ids from auth.JWTAuthenticator,
// short slug room_ids from gateway.defaultRooms), so the truncation is
// not triggered in practice. Revisit this parse — either by tightening
// the id parser or by reworking the partition-activator split — if a
// future authenticator or room-provisioning scheme introduces '$'.
//
// If the PID Id does not match the partition-activator shape (e.g., a
// future lookup provider with a different scheme), the function falls
// back to the full PID Id so an operator still has *something* to
// correlate on.
func grainID(ctx actor.ReceiverContext) string {
	self := ctx.Self()
	if self == nil {
		return ""
	}
	id := self.GetId()
	const prefix = "partition-activator/"
	if !strings.HasPrefix(id, prefix) {
		return id
	}
	trimmed := id[len(prefix):]
	if i := strings.LastIndex(trimmed, "$"); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}

// actorPath returns the full PID string for non-grain actors. For
// UserConnection this is the cluster path (e.g., "address/$1"); operators
// join on it the way they would on a grain_id.
func actorPath(ctx actor.ReceiverContext) string {
	self := ctx.Self()
	if self == nil {
		return ""
	}
	return self.String()
}

// typeName returns the Go type name of msg with any leading '*' trimmed.
// fmt.Sprintf("%T", &x) yields "*pkg.T"; the canonical log shape strips
// the pointer marker so consumers see "pkg.T" regardless of pointer-ness.
// This function never inspects message fields — NFR1 holds structurally.
func typeName(msg any) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", msg), "*")
}

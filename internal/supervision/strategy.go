// Package supervision provides a reusable protoactor SupervisorStrategy
// decorator that logs a structured supervision event carrying the failure
// envelope, then delegates directive application to one of Proto.Actor's
// built-in strategies.
//
// The classification (which cause maps to Resume/Restart/Stop/Escalate), the
// base log envelope, and the applying strategy are supplied by the caller.
// That keeps each call site's domain-specific error types and field names
// private while the logging machinery is shared here. The decider is the
// single place a reader looks to learn how an actor's failures are handled;
// this package only adds the structured log line in front of the runtime's
// own directive handling.
package supervision

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/asynkron/protoactor-go/actor"
)

// AttrFunc returns the base structured-log attributes for a supervision event.
// It receives the failed child PID and the message (already unwrapped from any
// envelope) the child was processing when it panicked.
type AttrFunc func(child *actor.PID, message any) []slog.Attr

// Config collects the collaborators NewLoggingStrategy requires.
type Config struct {
	// Event is the structured-log event name (the slog msg) of the
	// supervision line.
	Event string
	// Decider labels the log line with the directive the failure maps to. It
	// must mirror the policy Apply implements: build Apply from the same
	// decider via actor.NewOneForOneStrategy, or pair an always-Restart
	// decider with actor.NewRestartingStrategy, so the two cannot drift.
	Decider actor.DeciderFunc
	// Attrs returns the caller's base log attributes for the failure.
	Attrs AttrFunc
	// Logger receives the supervision line; nil falls back to slog.Default()
	// at log time.
	Logger *slog.Logger
	// Apply is the Proto.Actor strategy that applies the directive — and with
	// it the runtime's restart bookkeeping and SupervisorEvent publication —
	// after the log line is emitted.
	Apply actor.SupervisorStrategy
}

// NewLoggingStrategy builds the decorator. Every collaborator except Logger
// is required so an invalid strategy cannot survive construction and panic
// later while handling a child failure.
func NewLoggingStrategy(cfg Config) (actor.SupervisorStrategy, error) {
	if strings.TrimSpace(cfg.Event) == "" {
		return nil, fmt.Errorf("supervision event must not be empty")
	}
	if cfg.Decider == nil {
		return nil, fmt.Errorf("supervision decider must not be nil")
	}
	if cfg.Attrs == nil {
		return nil, fmt.Errorf("supervision attrs must not be nil")
	}
	if cfg.Apply == nil {
		return nil, fmt.Errorf("supervision apply strategy must not be nil")
	}
	return &loggingStrategy{cfg: cfg}, nil
}

type loggingStrategy struct {
	cfg Config
}

// HandleFailure logs the supervision envelope, then hands the failure to the
// wrapped Proto.Actor strategy to apply the directive.
func (s *loggingStrategy) HandleFailure(system *actor.ActorSystem, supervisor actor.Supervisor, child *actor.PID, rs *actor.RestartStatistics, reason, message any) {
	msg := actor.UnwrapEnvelopeMessage(message)
	directive := s.cfg.Decider(reason)

	baseAttrs := s.cfg.Attrs(child, msg)
	attrs := make([]slog.Attr, 0, len(baseAttrs)+3)
	attrs = append(attrs, baseAttrs...)
	attrs = append(attrs,
		slog.String("msg_type", typeName(msg)),
		slog.String("directive", directive.String()),
		slog.String("error", errText(reason)),
	)
	logAt(s.log(), directive, s.cfg.Event, attrs)

	s.cfg.Apply.HandleFailure(system, supervisor, child, rs, reason, message)
}

func (s *loggingStrategy) log() *slog.Logger {
	if s.cfg.Logger != nil {
		return s.cfg.Logger
	}
	return slog.Default()
}

// logAt picks a severity that matches the directive: a skipped (Resume) or
// restarted message is recoverable and logs at warn; a stopped or escalated
// child is terminal and logs at error.
func logAt(logger *slog.Logger, directive actor.Directive, event string, attrs []slog.Attr) {
	level := slog.LevelError
	switch directive {
	case actor.ResumeDirective, actor.RestartDirective:
		level = slog.LevelWarn
	}
	logger.LogAttrs(context.Background(), level, event, attrs...)
}

// typeName is the Go type name of msg with any leading '*' trimmed. It never
// inspects message fields, so the supervision envelope cannot leak payloads.
func typeName(msg any) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", msg), "*")
}

// errText renders a panic reason as a short string for the "error" field. An
// error uses its Error() message, a string is used verbatim, and a
// fmt.Stringer uses its String(). Any other value is reduced to its type name,
// so a panic value's fields never reach the log.
func errText(reason any) string {
	switch r := reason.(type) {
	case nil:
		return ""
	case error:
		return r.Error()
	case string:
		return r
	case fmt.Stringer:
		return r.String()
	default:
		return fmt.Sprintf("%T", reason)
	}
}

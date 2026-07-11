package logging

import (
	"context"
	"log/slog"
)

// NewMinLevelHandler returns a slog.Handler that drops records below min and
// delegates everything at or above min to inner. It raises a handler's
// effective floor without replacing its formatting or destination.
//
// blabby uses it to route proto.actor's internal logger through the process
// JSON stream while suppressing its Info chatter: wrapping slog.Default()'s
// handler at Warn keeps proto.actor's warnings and errors as JSON lines and
// discards the rest.
//
// WithAttrs and WithGroup wrap the inner handler's derived result and preserve
// the floor, so attributes and groups added through a derived logger survive.
func NewMinLevelHandler(inner slog.Handler, min slog.Level) slog.Handler {
	return minLevelHandler{inner: inner, min: min}
}

// minLevelHandler is the immutable slog.Handler decorator NewMinLevelHandler
// returns. Every derivation produces a new value rather than mutating the
// receiver.
type minLevelHandler struct {
	inner slog.Handler
	min   slog.Level
}

func (h minLevelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if level < h.min {
		return false
	}
	return h.inner.Enabled(ctx, level)
}

func (h minLevelHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < h.min {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

func (h minLevelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return minLevelHandler{inner: h.inner.WithAttrs(attrs), min: h.min}
}

func (h minLevelHandler) WithGroup(name string) slog.Handler {
	return minLevelHandler{inner: h.inner.WithGroup(name), min: h.min}
}

package auth

import (
	"context"

	"github.com/oklahomer/blabby/internal/id"
)

// contextKey is an unexported type used as the key for user-ID values stored
// in a context.Context. Using a struct (rather than a string) prevents
// collisions with any caller-defined string-typed key.
type contextKey struct{ name string }

// userIDContextKey is the package-level singleton used to identify the
// authenticated user ID injected by HTTP/WebSocket auth flows.
var userIDContextKey = &contextKey{name: "user_id"}

// ContextWithUserID returns a copy of ctx carrying the given authenticated
// user ID. Transport layers (HTTP middleware, WebSocket auth) call this after
// validating credentials so downstream handlers can recover the caller's
// identity without re-validating the token.
//
// This is intentionally a narrow bridge between the transport layer and the
// handler that immediately follows it. Once a handler has the user ID, it
// should pass it as an explicit argument to anything deeper in the stack
// (grains, persistence, business logic) rather than threading the context
// through and calling UserIDFromContext again — explicit arguments keep
// dependencies visible at function boundaries.
func ContextWithUserID(ctx context.Context, userID id.UserID) context.Context {
	return context.WithValue(ctx, userIDContextKey, userID)
}

// UserIDFromContext returns the authenticated user ID previously stored by
// ContextWithUserID. The boolean is true only when a value was set under the
// auth package's own key; values stored under foreign keys are ignored even
// if their underlying types match.
//
// Intended caller: HTTP handlers and WebSocket message handlers running
// directly behind the auth middleware. Layers that do not own a transport
// boundary (room/user grains, persistence, anything reachable via an actor
// message) should accept userID as an explicit parameter instead of reading
// it from a context they happen to have a reference to. If ok is false at a
// call site that runs behind authMiddleware, that is a wiring bug (route
// registered without g.requireAuth), not a runtime condition to recover from.
func UserIDFromContext(ctx context.Context) (id.UserID, bool) {
	v, ok := ctx.Value(userIDContextKey).(id.UserID)
	if !ok {
		return id.UserID{}, false
	}
	return v, true
}

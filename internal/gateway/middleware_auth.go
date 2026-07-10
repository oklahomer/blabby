package gateway

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/oklahomer/blabby/internal/auth"
)

// bearerPrefix is the strict, case-sensitive scheme prefix required by
// RFC 6750 §2.1. Any deviation (lowercase, alternate scheme, missing space)
// is treated as a missing token.
const bearerPrefix = "Bearer "

// authMiddleware validates the Authorization header on incoming requests and
// injects the authenticated user ID into the request context before calling
// next. Failures are returned via the gateway error envelope:
//   - missing/malformed Authorization header → 401 / 1003 (AUTH_MISSING_TOKEN)
//   - expired token → 401 / 1002 (AUTH_EXPIRED_TOKEN)
//   - identity backend unavailable → 503 / 5002 (SERVICE_UNAVAILABLE)
//   - any other validation failure → 401 / 1001 (AUTH_INVALID_TOKEN)
//
// The token, the Authorization header value, and the underlying authenticator
// error string are never written to logs — only a coarse reason classification
// (missing_or_malformed_header / expired / identity_unavailable / invalid) and
// request metadata.
func (g *Gateway) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := extractBearerToken(r.Header.Get("Authorization"))
		if !ok {
			slog.Warn("gateway.auth.rejected",
				"method", r.Method,
				"path", r.URL.Path,
				"reason", "missing_or_malformed_header",
				"remote_addr", r.RemoteAddr,
			)
			WriteErrorResponse(w, http.StatusUnauthorized,
				ErrAuthMissingToken("missing authorization header"))
			return
		}

		claims, err := g.auth.ValidateToken(r.Context(), token)
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrTokenExpired):
				slog.Warn("gateway.auth.rejected",
					"method", r.Method,
					"path", r.URL.Path,
					"reason", "expired",
					"remote_addr", r.RemoteAddr,
				)
				WriteErrorResponse(w, http.StatusUnauthorized,
					ErrAuthExpiredToken("token has expired"))
			case errors.Is(err, auth.ErrIdentityUnavailable):
				// The token is well-formed but its identity could not be resolved
				// because the account backend is unavailable. Answer 503 (retry) so
				// a transient outage does not force clients to discard valid tokens.
				slog.Error("gateway.auth.backend_unavailable",
					"method", r.Method,
					"path", r.URL.Path,
					"reason", "identity_unavailable",
					"remote_addr", r.RemoteAddr,
				)
				WriteErrorResponse(w, http.StatusServiceUnavailable,
					ErrServiceUnavailable("authentication temporarily unavailable"))
			default:
				slog.Warn("gateway.auth.rejected",
					"method", r.Method,
					"path", r.URL.Path,
					"reason", "invalid",
					"remote_addr", r.RemoteAddr,
				)
				WriteErrorResponse(w, http.StatusUnauthorized,
					ErrAuthInvalidToken("invalid token"))
			}
			return
		}

		// Defensive: a misbehaving authenticator that returns (nil, nil)
		// would otherwise authenticate the request with no identity. Same
		// client-facing response as a malformed token so attackers cannot
		// distinguish the cases; operators get the slog.Error signal.
		if claims == nil {
			slog.Error("gateway.auth.contract_violation",
				"method", r.Method,
				"path", r.URL.Path,
				"reason", "authenticator_contract_violation",
				"remote_addr", r.RemoteAddr,
			)
			WriteErrorResponse(w, http.StatusUnauthorized,
				ErrAuthInvalidToken("invalid token"))
			return
		}

		ctx := auth.ContextWithUserID(r.Context(), claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAuth wraps an http.HandlerFunc with authMiddleware so route
// registrations can opt a handler into authentication with a single call:
//
//	mux.Handle("PUT /rooms/{id}/membership", g.requireAuth(g.handleRoomMembershipPut))
//
// The wrapper returns an http.Handler (not HandlerFunc), which is why callers
// must use mux.Handle rather than mux.HandleFunc.
func (g *Gateway) requireAuth(h http.HandlerFunc) http.Handler {
	return g.authMiddleware(h)
}

// extractBearerToken parses the value of an Authorization header and returns
// the bearer token if the header conforms strictly to "Bearer <token>" per
// RFC 6750 §2.1. Any deviation — wrong scheme, lowercase prefix, empty token,
// or whitespace within the token — yields ok=false so callers can respond
// with AUTH_MISSING_TOKEN without leaking which exact substring was wrong.
func extractBearerToken(h string) (string, bool) {
	if len(h) <= len(bearerPrefix) || h[:len(bearerPrefix)] != bearerPrefix {
		return "", false
	}
	token := h[len(bearerPrefix):]
	if token == "" {
		return "", false
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/oklahomer/blabby/internal/auth"
)

const (
	maxLoginBodyBytes = 1 << 20 // 1 MB
	maxUsernameBytes  = 64
	maxPasswordBytes  = 256
)

// endpointLogin is the mux pattern for handleLogin. Defined alongside
// the handler so the route table in RegisterRoutes references the same
// string the handler is registered under.
const endpointLogin = "POST /login"

// LoginRequest is the JSON payload accepted by POST /login.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginResponse is the JSON payload returned for a successful login.
type LoginResponse struct {
	Token string `json:"token"`
}

func (g *Gateway) handleLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)

	var req LoginRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("malformed request body"))
		return
	}
	// Reject any trailing data after the first JSON value to avoid silently
	// accepting concatenated objects or junk after a valid request.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("malformed request body"))
		return
	}

	if strings.TrimSpace(req.Username) == "" || strings.TrimSpace(req.Password) == "" {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("username and password are required"))
		return
	}
	if len(req.Username) > maxUsernameBytes || len(req.Password) > maxPasswordBytes {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("username or password exceeds maximum length"))
		return
	}

	result, err := g.auth.Authenticate(r.Context(), auth.AuthParams{
		Username: req.Username,
		Password: req.Password,
	})
	if err != nil {
		// Log server-side so operators can distinguish bad credentials from
		// infrastructure failures. Never log the username or password.
		slog.Warn("login authentication failed", "error", err.Error())
		// All authentication failures are reported as invalid credentials to
		// prevent user enumeration.
		WriteErrorResponse(w, http.StatusUnauthorized, ErrAuthInvalidToken("invalid credentials"))
		return
	}
	if result == nil || result.Token == "" {
		// Defensive: a buggy authenticator that returns success with no token
		// must not produce a 200 with an empty bearer.
		slog.Error("authenticator returned no token on success path")
		WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("authentication unavailable"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(LoginResponse{Token: result.Token}); err != nil {
		slog.Error("failed to write login response", "error", err)
	}
}

// handleMethodNotAllowed returns a handler that responds 405 with the
// given Allow header. Used as the mux fallback for path-only patterns
// (e.g. "/login" alongside "POST /login"); Go 1.22+ mux dispatches the
// more specific method+path pattern first and falls through here when
// the method does not match.
func (g *Gateway) handleMethodNotAllowed(allowed string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allowed)
		WriteErrorResponse(w, http.StatusMethodNotAllowed, ErrInvalidRequest("method not allowed"))
	}
}

// handleNotFound responds with a JSON error envelope for any unmatched path,
// keeping the gateway response shape consistent across error cases.
func (g *Gateway) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	WriteErrorResponse(w, http.StatusNotFound, ErrInvalidRequest("not found"))
}

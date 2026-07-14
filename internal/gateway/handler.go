package gateway

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/domain"
)

const (
	maxLoginBodyBytes = 1 << 20 // 1 MB
	maxPasswordBytes  = 256
)

// endpointLogin is the mux pattern for handleLogin. Defined alongside
// the handler so the route table in RegisterRoutes references the same
// string the handler is registered under.
const endpointLogin = "POST /login"

// LoginRequest is the JSON payload accepted by POST /login. Login identity is the
// account's email; the issued token carries the user's U… public_code, never the
// internal numeric id.
type LoginRequest struct {
	MailAddress string `json:"mail_address"`
	Password    string `json:"password"`
}

// LoginResponse is the JSON payload returned for a successful login.
type LoginResponse struct {
	Token string `json:"token"`
}

func (g *Gateway) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if !decodeJSONBody(w, r, maxLoginBodyBytes, &req) {
		return
	}

	if strings.TrimSpace(req.MailAddress) == "" || strings.TrimSpace(req.Password) == "" {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("mail address and password are required"))
		return
	}
	if len(req.MailAddress) > domain.MaxMailAddressBytes || len(req.Password) > maxPasswordBytes {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("mail address or password exceeds maximum length"))
		return
	}

	result, err := g.auth.Authenticate(r.Context(), auth.AuthParams{
		MailAddress: req.MailAddress,
		Password:    req.Password,
	})
	if err != nil {
		// Most credential rejections are reported uniformly as invalid credentials
		// to prevent user enumeration. The one distinguished case is a pending
		// account after a correct password proves ownership; infrastructure
		// failures are 500s, not misleading 401s. Never log the mail address or
		// password.
		if errors.Is(err, auth.ErrAccountPending) {
			// The password verified, so naming the pending state reveals nothing
			// an account owner does not already know; the client uses the
			// distinct status to route the user to email verification.
			slog.Warn("gateway.login.rejected", "reason", "account_pending")
			WriteErrorResponse(w, http.StatusUnauthorized,
				ErrAuthAccountPending("verify your email to finish creating your account"))
			return
		}
		if errors.Is(err, auth.ErrInvalidCredentials) {
			slog.Warn("gateway.login.rejected", "reason", "invalid_credentials")
			WriteErrorResponse(w, http.StatusUnauthorized, ErrAuthInvalidToken("invalid credentials"))
			return
		}
		slog.Error("gateway.login.error", "error", err.Error())
		WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("authentication unavailable"))
		return
	}
	if result == nil || result.Token == "" {
		// Defensive: a buggy authenticator that returns success with no token
		// must not produce a 200 with an empty bearer.
		slog.Error("gateway.login.contract_violation")
		WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("authentication unavailable"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(LoginResponse{Token: result.Token}); err != nil {
		slog.Error("gateway.login.write_failed", "error", err)
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

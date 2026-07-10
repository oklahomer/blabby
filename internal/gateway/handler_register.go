package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/persistence"
)

// endpointRegister is the mux pattern for handleRegister.
const endpointRegister = "POST /users"

// RegisterRequest is the JSON payload accepted by POST /users.
type RegisterRequest struct {
	MailAddress string `json:"mail_address"`
	Handle      string `json:"handle"`
	Password    string `json:"password"`
}

// RegisterResponse is the JSON payload returned for a successful registration: the
// new (or already-pending) account's client-facing U… code.
type RegisterResponse struct {
	PublicCode string `json:"public_code"`
}

// handleRegister creates a pending account and dispatches its verification PIN.
// It is unauthenticated. Validation maps to specific codes (INVALID_EMAIL,
// WEAK_PASSWORD, a generic INVALID_REQUEST for a malformed handle); a duplicate
// address/handle is a 409, an exhausted resend budget a 429; success is 201.
func (g *Gateway) handleRegister(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)

	var req RegisterRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("malformed request body"))
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("malformed request body"))
		return
	}

	if strings.TrimSpace(req.MailAddress) == "" || strings.TrimSpace(req.Handle) == "" || req.Password == "" {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("mail_address, handle, and password are required"))
		return
	}
	if len(req.Password) > maxPasswordBytes {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("password exceeds maximum length"))
		return
	}

	mail, err := domain.NewMailAddress(req.MailAddress)
	if err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidEmail("mail address is not a valid email"))
		return
	}
	handle, err := domain.NewHandle(req.Handle)
	if err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("handle must be 3-30 characters of letters, digits, or underscore"))
		return
	}
	if err := auth.ValidatePasswordStrength(req.Password); err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, ErrWeakPassword("password is too short"))
		return
	}

	result, err := g.registration.Register(r.Context(), RegisterParams{
		MailAddress: mail,
		Handle:      handle,
		Password:    req.Password,
	})
	if err != nil {
		g.writeRegisterError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(RegisterResponse(result)); err != nil {
		slog.Error("gateway.registration.write_failed", "error", err)
	}
}

// writeRegisterError maps a registration service error to its client response. A
// taken email or handle is a 409 with its distinct status; an exhausted resend
// budget is a 429; anything else is a server error with no internal detail.
func (g *Gateway) writeRegisterError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, persistence.ErrMailAddressTaken):
		WriteErrorResponse(w, http.StatusConflict, ErrEmailAlreadyRegistered("email already registered"))
	case errors.Is(err, persistence.ErrHandleTaken):
		WriteErrorResponse(w, http.StatusConflict, ErrHandleAlreadyTaken("handle already taken"))
	case errors.Is(err, persistence.ErrVerificationRateLimited):
		WriteErrorResponse(w, http.StatusTooManyRequests, ErrVerificationRateLimited("too many verification attempts; please wait and try again"))
	default:
		slog.Error("gateway.registration.failed", "error", err.Error())
		WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("registration unavailable"))
	}
}

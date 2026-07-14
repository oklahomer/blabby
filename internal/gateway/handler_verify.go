package gateway

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/persistence"
)

const (
	// endpointVerify activates a pending account by its emailed PIN.
	endpointVerify = "POST /users/verifications"
	// endpointResendVerification re-issues a PIN to a pending account.
	endpointResendVerification = "POST /users/verifications/resend"

	// maxPINBytes caps the submitted PIN field. A real PIN is short, so anything
	// longer is a malformed request, not a verification attempt.
	maxPINBytes = 16
)

// VerifyRequest is the JSON payload accepted by POST /users/verifications.
type VerifyRequest struct {
	MailAddress string `json:"mail_address"`
	PIN         string `json:"pin"`
}

// ResendVerificationRequest is the JSON payload accepted by
// POST /users/verifications/resend.
type ResendVerificationRequest struct {
	MailAddress string `json:"mail_address"`
}

// handleVerify activates a pending account when the submitted PIN matches its
// challenge. It is unauthenticated. Failure is uniform — 400 VERIFICATION_INVALID
// for an unknown account, non-pending account, wrong PIN, expired challenge, or
// locked challenge — so it reveals nothing about which precondition failed.
// Success is 200.
func (g *Gateway) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req VerifyRequest
	if !decodeJSONBody(w, r, maxLoginBodyBytes, &req) {
		return
	}

	if strings.TrimSpace(req.MailAddress) == "" || req.PIN == "" {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("mail_address and pin are required"))
		return
	}
	if len(req.PIN) > maxPINBytes {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("pin exceeds maximum length"))
		return
	}
	mail, err := domain.NewMailAddress(req.MailAddress)
	if err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidEmail("mail address is not a valid email"))
		return
	}

	if err := g.verification.Verify(r.Context(), VerifyParams{MailAddress: mail, PIN: req.PIN}); err != nil {
		g.writeVerifyError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// writeVerifyError maps a verification outcome to its client response.
func (g *Gateway) writeVerifyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errVerifyInvalid) {
		WriteErrorResponse(w, http.StatusBadRequest, ErrVerificationInvalid("verification failed"))
		return
	}
	slog.Error("gateway.verification.failed", "error", err.Error())
	WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("verification unavailable"))
}

// handleResendVerification re-issues a PIN to a pending account. It is
// unauthenticated and returns 200 for an unknown or already-active address (a
// silent no-op) so it cannot be used to probe which addresses are registered; only
// a pending account that has exhausted its resend budget yields 429.
func (g *Gateway) handleResendVerification(w http.ResponseWriter, r *http.Request) {
	var req ResendVerificationRequest
	if !decodeJSONBody(w, r, maxLoginBodyBytes, &req) {
		return
	}

	if strings.TrimSpace(req.MailAddress) == "" {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("mail_address is required"))
		return
	}
	mail, err := domain.NewMailAddress(req.MailAddress)
	if err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidEmail("mail address is not a valid email"))
		return
	}

	if err := g.verification.Resend(r.Context(), ResendParams{MailAddress: mail}); err != nil {
		if errors.Is(err, persistence.ErrVerificationRateLimited) {
			WriteErrorResponse(w, http.StatusTooManyRequests, ErrVerificationRateLimited("too many resend requests; please wait and try again"))
			return
		}
		slog.Error("gateway.verification.resend_failed", "error", err.Error())
		WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("verification unavailable"))
		return
	}
	w.WriteHeader(http.StatusOK)
}

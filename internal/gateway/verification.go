package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/persistence"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/persistence/verifyrepo"
	"github.com/oklahomer/blabby/internal/verification"
)

// VerifyParams are the validated inputs of a PIN verification: a parsed email and
// the raw PIN as submitted (the service parses and compares it).
type VerifyParams struct {
	MailAddress domain.MailAddress
	PIN         string
}

// ResendParams are the validated inputs of an explicit PIN resend.
type ResendParams struct {
	MailAddress domain.MailAddress
}

// VerificationService verifies a submitted PIN (activating the account on success)
// and resends a fresh PIN to a pending account. The gateway handlers depend on this
// interface so they can be unit-tested with a fake; RegistrationService is the
// production implementation.
type VerificationService interface {
	Verify(ctx context.Context, params VerifyParams) error
	Resend(ctx context.Context, params ResendParams) error
}

// errVerifyInvalid is the uniform failure for a verification that cannot succeed:
// no pending account/challenge, an expired or locked challenge, or a wrong PIN.
// Collapsing these into one outcome avoids revealing which precondition failed.
var errVerifyInvalid = errors.New("gateway: verification invalid")

// Verify checks a submitted PIN against the account's pending challenge and, on a
// match, activates the account and clears the challenge.
//
// The whole check runs in one transaction. Business outcomes are reported via the
// returned error while the closure returns nil, so the failed-attempt increment
// commits alongside the reads; only an infrastructure error returned from the
// closure rolls the transaction back and surfaces as a server error to the caller.
func (s *RegistrationService) Verify(ctx context.Context, params VerifyParams) error {
	var outcome error
	invalid := func() error {
		outcome = errVerifyInvalid
		return nil
	}

	txErr := s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		user, err := s.users.FindByEmail(ctx, q, params.MailAddress)
		if err != nil {
			if errors.Is(err, persistence.ErrUserNotFound) {
				return invalid()
			}
			return fmt.Errorf("verify: find user: %w", err)
		}
		if user.Status != domain.UserStatusPending {
			return invalid()
		}
		challenge, err := s.verify.FindByUser(ctx, q, user.ID)
		if err != nil {
			if errors.Is(err, verifyrepo.ErrVerificationNotFound) {
				return invalid()
			}
			return fmt.Errorf("verify: find challenge: %w", err)
		}
		if challenge.Attempts >= s.policy.MaxVerifyAttempts {
			return invalid()
		}
		if challenge.Expired(s.now()) {
			return invalid()
		}
		if err := verification.Verify(challenge.PinHash, params.PIN); err != nil {
			if _, err := s.verify.IncrementAttempts(ctx, q, user.ID); err != nil {
				return fmt.Errorf("verify: increment attempts: %w", err)
			}
			return invalid()
		}
		if err := s.users.SetStatus(ctx, q, user.ID, domain.UserStatusActive); err != nil {
			return fmt.Errorf("verify: activate account: %w", err)
		}
		if err := s.verify.Delete(ctx, q, user.ID); err != nil {
			return fmt.Errorf("verify: clear challenge: %w", err)
		}
		outcome = nil
		return nil
	})
	if txErr != nil {
		return txErr
	}
	return outcome
}

// Resend issues a fresh PIN to a pending account, enforcing the resend budget. To
// avoid revealing whether an address is registered (or already active), it returns
// nil for an unknown or non-pending address — a silent no-op. A pending account at
// its resend budget returns verifyrepo.ErrVerificationRateLimited. Delivery after
// commit is best-effort, like registration.
func (s *RegistrationService) Resend(ctx context.Context, params ResendParams) error {
	var toSend pendingSend
	var shouldSend bool

	txErr := s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		shouldSend = false
		user, err := s.users.FindByEmail(ctx, q, params.MailAddress)
		if err != nil {
			if errors.Is(err, persistence.ErrUserNotFound) {
				return nil
			}
			return fmt.Errorf("resend: find user: %w", err)
		}
		if user.Status != domain.UserStatusPending {
			return nil
		}
		pin, err := s.resendPending(ctx, q, user.ID)
		if err != nil {
			return err
		}
		toSend = pendingSend{to: params.MailAddress, pin: pin}
		shouldSend = true
		return nil
	})
	if txErr != nil {
		return txErr
	}
	if shouldSend {
		if err := s.sender.Send(ctx, toSend.to, toSend.pin, s.policy.PinTTL); err != nil {
			slog.Error("resend: pin delivery failed", "error", err)
		}
	}
	return nil
}

package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/verification"
)

// RegisterParams are the validated inputs of a registration: the handler has
// already parsed the email and handle and checked the password's strength.
type RegisterParams struct {
	MailAddress domain.MailAddress
	Handle      domain.Handle
	Password    string
}

// RegisterResult is the outcome of a successful registration (or a resend to an
// existing pending account): the account's client-facing U… code.
type RegisterResult struct {
	PublicCode string
}

// Registrar creates a pending account and dispatches its verification PIN, or
// resends to an already-pending account. The gateway handler depends on this
// interface so it can be unit-tested with a fake; RegistrationService is the
// production implementation.
type Registrar interface {
	Register(ctx context.Context, params RegisterParams) (RegisterResult, error)
}

// transactor runs fn inside a database transaction. *postgres.Transactor
// satisfies it; the registration write (account row + verification challenge) runs
// as one unit so a pending account never exists without its challenge.
type transactor interface {
	WithinTx(ctx context.Context, fn func(q postgres.Querier) error) error
}

type registrationUsers interface {
	FindByEmail(ctx context.Context, q postgres.Querier, mail domain.MailAddress) (persistence.User, error)
	Create(ctx context.Context, q postgres.Querier, params persistence.UserCreateParams) (persistence.User, error)
	SetStatus(ctx context.Context, q postgres.Querier, userID id.UserID, status domain.UserStatus) error
}

type registrationVerifications interface {
	Create(ctx context.Context, q postgres.Querier, params persistence.VerificationCreateParams) error
	Resend(ctx context.Context, q postgres.Querier, params persistence.VerificationResendParams, policy persistence.VerificationResendPolicy) error
	FindByUser(ctx context.Context, q postgres.Querier, userID id.UserID) (persistence.Verification, error)
	IncrementAttempts(ctx context.Context, q postgres.Querier, userID id.UserID) (int, error)
	Delete(ctx context.Context, q postgres.Querier, userID id.UserID) error
}

// RegistrationPolicy bounds the registration/resend behavior.
type RegistrationPolicy struct {
	// PinTTL is how long an issued PIN stays valid.
	PinTTL time.Duration
	// ResendMinInterval is the minimum spacing between sends to one account.
	ResendMinInterval time.Duration
	// MaxResendCount caps how many times a pending account's PIN may be re-issued.
	MaxResendCount int
	// MaxVerifyAttempts caps wrong PIN submissions before the challenge locks; the
	// user must request a new code, and a resend resets the counter.
	MaxVerifyAttempts int
	// CollisionRetries bounds re-running the create transaction when a minted
	// public_code collides with an existing row.
	CollisionRetries int
}

// DefaultRegistrationPolicy is the production policy.
func DefaultRegistrationPolicy() RegistrationPolicy {
	return RegistrationPolicy{
		PinTTL:            10 * time.Minute,
		ResendMinInterval: 60 * time.Second,
		MaxResendCount:    5,
		MaxVerifyAttempts: 5,
		CollisionRetries:  3,
	}
}

// RegistrationService is the production Registrar. It writes the pending account
// and its verification challenge in one transaction, then delivers the PIN
// best-effort after the commit — outside the transaction, so a slow sender never
// holds a database connection.
type RegistrationService struct {
	users  registrationUsers
	verify registrationVerifications
	sender verification.Sender
	tx     transactor
	now    func() time.Time
	policy RegistrationPolicy
}

// NewRegistrationService builds a RegistrationService. users must mint ids (its
// IDSource is the gateway's worker-lease manager), since registration creates new
// accounts.
func NewRegistrationService(users *persistence.UserRepo, verify *persistence.VerificationRepo, sender verification.Sender, tx transactor, policy RegistrationPolicy) *RegistrationService {
	return &RegistrationService{
		users:  users,
		verify: verify,
		sender: sender,
		tx:     tx,
		now:    time.Now,
		policy: policy,
	}
}

// pendingSend captures the PIN to deliver once the transaction has committed, so
// delivery (an external side effect) happens outside the transaction.
type pendingSend struct {
	to  domain.MailAddress
	pin verification.PIN
}

// Register creates a pending account and issues its PIN, or — for an address that
// is already pending — resends a fresh PIN (rate-limited), ignoring the new handle
// and password so a re-registration cannot mutate the pending record. An address
// owned by an active or disabled account is rejected with
// persistence.ErrMailAddressTaken. PIN delivery after commit is best-effort: a send
// failure is logged, not surfaced, since the account exists and the user can resend.
func (s *RegistrationService) Register(ctx context.Context, params RegisterParams) (RegisterResult, error) {
	var result RegisterResult
	var toSend pendingSend
	var shouldSend bool

	op := func(q postgres.Querier) error {
		shouldSend = false
		existing, err := s.users.FindByEmail(ctx, q, params.MailAddress)
		switch {
		case err == nil:
			if existing.Status != domain.UserStatusPending {
				return persistence.ErrMailAddressTaken
			}
			pin, err := s.resendPending(ctx, q, existing.ID)
			if err != nil {
				return err
			}
			result = RegisterResult{PublicCode: existing.PublicID()}
			toSend = pendingSend{to: params.MailAddress, pin: pin}
			shouldSend = true
			return nil
		case errors.Is(err, persistence.ErrUserNotFound):
			passwordHash, err := auth.HashPassword(params.Password)
			if err != nil {
				return fmt.Errorf("registration: hash password: %w", err)
			}
			user, pin, err := s.createPending(ctx, q, params, passwordHash)
			if err != nil {
				return err
			}
			result = RegisterResult{PublicCode: user.PublicID()}
			toSend = pendingSend{to: params.MailAddress, pin: pin}
			shouldSend = true
			return nil
		default:
			return fmt.Errorf("registration: find by email: %w", err)
		}
	}

	if err := s.runWithRegistrationRetry(ctx, op); err != nil {
		return RegisterResult{}, err
	}
	if shouldSend {
		if err := s.sender.Send(ctx, toSend.to, toSend.pin, s.policy.PinTTL); err != nil {
			slog.Error("registration: pin delivery failed", "public_code", result.PublicCode, "error", err)
		}
	}
	return result, nil
}

// createPending mints the account (status pending) and stores its hashed PIN in
// one transaction step. It returns the new user and the plaintext PIN to deliver.
func (s *RegistrationService) createPending(ctx context.Context, q postgres.Querier, params RegisterParams, passwordHash []byte) (persistence.User, verification.PIN, error) {
	pin, pinHash, err := newHashedPIN()
	if err != nil {
		return persistence.User{}, verification.PIN{}, err
	}
	user, err := s.users.Create(ctx, q, persistence.UserCreateParams{
		MailAddress:  params.MailAddress,
		Handle:       params.Handle,
		DisplayName:  params.Handle.Display(),
		PasswordHash: passwordHash,
		Status:       domain.UserStatusPending,
	})
	if err != nil {
		if errors.Is(err, persistence.ErrMailAddressTaken) {
			return persistence.User{}, verification.PIN{}, fmt.Errorf("%w: %w", errMailAddressInsertRace, err)
		}
		return persistence.User{}, verification.PIN{}, err
	}
	now := s.now()
	if err := s.verify.Create(ctx, q, persistence.VerificationCreateParams{
		UserID:    user.ID,
		PinHash:   pinHash,
		ExpiresAt: now.Add(s.policy.PinTTL),
		SentAt:    now,
	}); err != nil {
		return persistence.User{}, verification.PIN{}, fmt.Errorf("registration: store verification: %w", err)
	}
	return user, pin, nil
}

// resendPending issues a fresh PIN for an already-pending account, enforcing the
// resend budget. It returns persistence.ErrVerificationRateLimited when the budget
// or minimum interval is exhausted.
func (s *RegistrationService) resendPending(ctx context.Context, q postgres.Querier, userID id.UserID) (verification.PIN, error) {
	pin, pinHash, err := newHashedPIN()
	if err != nil {
		return verification.PIN{}, err
	}
	now := s.now()
	err = s.verify.Resend(ctx, q, persistence.VerificationResendParams{
		UserID:    userID,
		PinHash:   pinHash,
		ExpiresAt: now.Add(s.policy.PinTTL),
		SentAt:    now,
	}, persistence.VerificationResendPolicy{
		PreviousSentBefore: now.Add(-s.policy.ResendMinInterval),
		MaxResendCount:     s.policy.MaxResendCount,
	})
	if err != nil {
		return verification.PIN{}, err
	}
	return pin, nil
}

var errMailAddressInsertRace = errors.New("registration: mail address insert raced")

// runWithRegistrationRetry re-runs op (the create/resend transaction) when it
// fails with a recoverable public_code collision, minting a fresh code each
// attempt. It also retries once when the initial no-user read races another insert
// for the same email; the retry observes the winner and either resends to a
// pending account or returns ErrMailAddressTaken for an active one.
func (s *RegistrationService) runWithRegistrationRetry(ctx context.Context, op func(q postgres.Querier) error) error {
	retriedEmailRace := false
	publicCodeCollisions := 0
	for {
		err := s.tx.WithinTx(ctx, op)
		switch {
		case errors.Is(err, errMailAddressInsertRace) && !retriedEmailRace:
			retriedEmailRace = true
			continue
		case errors.Is(err, persistence.ErrUserPublicCodeCollision):
			if publicCodeCollisions >= s.policy.CollisionRetries {
				return fmt.Errorf("registration: public_code collisions exhausted after %d retries: %w", publicCodeCollisions, err)
			}
			publicCodeCollisions++
			continue
		default:
			return err
		}
	}
}

// newHashedPIN generates a fresh PIN and its bcrypt hash for storage.
func newHashedPIN() (verification.PIN, []byte, error) {
	pin, err := verification.NewPIN()
	if err != nil {
		return verification.PIN{}, nil, fmt.Errorf("registration: generate pin: %w", err)
	}
	hash, err := pin.Hash()
	if err != nil {
		return verification.PIN{}, nil, fmt.Errorf("registration: hash pin: %w", err)
	}
	return pin, hash, nil
}

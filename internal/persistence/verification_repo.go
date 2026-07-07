package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// ErrVerificationNotFound is returned when no email_verification row exists for a
// user — the account was never pending, or its challenge was already consumed
// (deleted on success) or swept (pending GC).
var ErrVerificationNotFound = errors.New("persistence: verification not found")

// ErrVerificationRateLimited is returned when a resend would violate the durable
// resend budget recorded on the challenge row.
var ErrVerificationRateLimited = errors.New("persistence: verification resend rate limited")

// verificationColumns is the fixed projection scanVerification expects, in order.
const verificationColumns = `user_id, pin_hash, expires_at, attempts, resend_count, last_sent_at, created_at`

// VerificationRepo reads and writes the email_verification table. Its methods take a
// postgres.Querier (a pool or a transaction) per call so registration can compose
// the user INSERT and this challenge INSERT into one transaction.
type VerificationRepo struct{}

// NewVerificationRepo returns a VerificationRepo. It is stateless — email_verification rows are keyed by an
// already-minted user_id, so the repo mints nothing.
func NewVerificationRepo() *VerificationRepo { return &VerificationRepo{} }

// VerificationCreateParams carries the fields of a fresh verification challenge. PinHash is the
// already-hashed PIN; the repo never sees the plaintext.
type VerificationCreateParams struct {
	UserID    id.UserID
	PinHash   []byte
	ExpiresAt time.Time
	SentAt    time.Time
}

const createSQL = `
INSERT INTO email_verification (user_id, pin_hash, expires_at, attempts, resend_count, last_sent_at)
VALUES ($1, $2, $3, 0, 0, $4)`

// Create inserts a fresh challenge for a newly registered (pending) account:
// attempts and resend_count start at zero, last_sent_at records the initial send.
// A second Create for the same user collides on the primary key (a hard error);
// the resend path (re-register or POST resend) uses Resend instead.
func (r *VerificationRepo) Create(ctx context.Context, q postgres.Querier, params VerificationCreateParams) error {
	_, err := q.Exec(ctx, createSQL,
		params.UserID.Int64(), params.PinHash, params.ExpiresAt, params.SentAt)
	if err != nil {
		return fmt.Errorf("persistence: create: %w", err)
	}
	return nil
}

const findByUserSQL = `SELECT ` + verificationColumns + ` FROM email_verification WHERE user_id = $1`

// FindByUser loads the challenge for a user — the verify/resend read path. It
// returns ErrVerificationNotFound when the user has no pending challenge.
func (r *VerificationRepo) FindByUser(ctx context.Context, q postgres.Querier, userID id.UserID) (Verification, error) {
	v, err := scanVerification(q.QueryRow(ctx, findByUserSQL, userID.Int64()))
	if errors.Is(err, pgx.ErrNoRows) {
		return Verification{}, ErrVerificationNotFound
	}
	if err != nil {
		return Verification{}, fmt.Errorf("persistence: find by user: %w", err)
	}
	return v, nil
}

const incrementAttemptsSQL = `
UPDATE email_verification SET attempts = attempts + 1 WHERE user_id = $1 RETURNING attempts`

// IncrementAttempts records one failed PIN submission and returns the new attempt
// count, so the caller can lock the challenge once it reaches the cap. It returns
// ErrVerificationNotFound when no challenge exists for the user.
func (r *VerificationRepo) IncrementAttempts(ctx context.Context, q postgres.Querier, userID id.UserID) (int, error) {
	var attempts int
	err := q.QueryRow(ctx, incrementAttemptsSQL, userID.Int64()).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrVerificationNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("persistence: increment attempts: %w", err)
	}
	return attempts, nil
}

// VerificationResendParams carries the fields of a re-issued challenge: a fresh PIN hash and
// expiry, and the send time. The repo resets attempts and bumps resend_count.
type VerificationResendParams struct {
	UserID    id.UserID
	PinHash   []byte
	ExpiresAt time.Time
	SentAt    time.Time
}

// VerificationResendPolicy is the durable resend budget enforced by Resend. A resend is
// allowed only when the previous send happened no later than PreviousSentBefore and
// the current resend_count is still below MaxResendCount.
type VerificationResendPolicy struct {
	PreviousSentBefore time.Time
	MaxResendCount     int
}

const resendSQL = `
WITH updated AS (
	UPDATE email_verification
	SET pin_hash = $2, expires_at = $3, attempts = 0, last_sent_at = $4, resend_count = resend_count + 1
	WHERE user_id = $1
	  AND (last_sent_at IS NULL OR last_sent_at <= $5)
	  AND resend_count < $6
	RETURNING 1
),
existing AS (
	SELECT 1 FROM email_verification WHERE user_id = $1
)
SELECT EXISTS(SELECT 1 FROM updated), EXISTS(SELECT 1 FROM existing)`

// Resend replaces the challenge's PIN and expiry, clears the attempt lock, records
// the send time, and increments resend_count. The rate-limit policy is enforced
// inside the UPDATE so concurrent resend requests cannot all pass the same
// read-then-write check. It returns ErrVerificationNotFound when no challenge
// exists for the user and ErrVerificationRateLimited when the durable budget is
// exhausted or the minimum interval has not elapsed.
func (r *VerificationRepo) Resend(ctx context.Context, q postgres.Querier, params VerificationResendParams, policy VerificationResendPolicy) error {
	var updated, found bool
	err := q.QueryRow(ctx, resendSQL,
		params.UserID.Int64(), params.PinHash, params.ExpiresAt, params.SentAt,
		policy.PreviousSentBefore, policy.MaxResendCount,
	).Scan(&updated, &found)
	if err != nil {
		return fmt.Errorf("persistence: resend: %w", err)
	}
	if updated {
		return nil
	}
	if !found {
		return ErrVerificationNotFound
	}
	return ErrVerificationRateLimited
}

const deleteSQL = `DELETE FROM email_verification WHERE user_id = $1`

// Delete removes the challenge — on successful verification, or as part of the
// pending-account sweep. It returns ErrVerificationNotFound when no row carried the
// id, so a caller can tell a real delete from a no-op.
func (r *VerificationRepo) Delete(ctx context.Context, q postgres.Querier, userID id.UserID) error {
	tag, err := q.Exec(ctx, deleteSQL, userID.Int64())
	if err != nil {
		return fmt.Errorf("persistence: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrVerificationNotFound
	}
	return nil
}

// Package accountgc deletes abandoned pending accounts left by registrations that
// were never verified. It is the execution core of the pending-account GC job; the
// trigger (a scheduled call into a singleton maintenance grain) lives elsewhere.
package accountgc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// pendingAccountGCLockKey is the fixed key for the sweep's transaction-scoped
// advisory lock. Every process that runs the sweep uses this same key, so
// pg_try_advisory_xact_lock lets only one sweep proceed at a time across the whole
// cluster and the rest no-op. The value is arbitrary (ASCII "pending" as a
// mnemonic); it only needs to stay stable and not collide with another advisory
// lock in the database.
const pendingAccountGCLockKey int64 = 0x70656e64696e67

// ErrInvalidGrace reports that a sweep grace period cannot represent a valid
// retention window.
var ErrInvalidGrace = errors.New("accountgc: grace must not be negative")

// Transactor runs fn inside a database transaction. *postgres.Transactor satisfies
// it. The advisory lock is transaction-scoped, so the lock and the delete must run
// in the same transaction.
type Transactor interface {
	WithinTx(ctx context.Context, fn func(q postgres.Querier) error) error
}

// Sweeper deletes abandoned pending accounts. An account is swept once its
// verification challenge expired more than the grace period before the sweep time,
// so a resend — which extends expires_at — protects an account a user is still
// trying to verify.
type Sweeper struct {
	tx    Transactor
	grace time.Duration
}

// NewSweeper returns a Sweeper that deletes pending accounts whose challenge
// expired more than grace before the sweep time.
func NewSweeper(tx Transactor, grace time.Duration) (*Sweeper, error) {
	if grace < 0 {
		return nil, ErrInvalidGrace
	}
	return &Sweeper{tx: tx, grace: grace}, nil
}

const tryLockSQL = `SELECT pg_try_advisory_xact_lock($1)`

// sweepSQL deletes pending accounts whose challenge expired before the cutoff. The
// email_verification row is removed by its ON DELETE CASCADE foreign key.
const sweepSQL = `
DELETE FROM service_user u
WHERE u.status = 'pending'
  AND EXISTS (
      SELECT 1 FROM email_verification ev
      WHERE ev.user_id = u.id
        AND ev.expires_at < $1
  )`

// Sweep deletes pending accounts whose challenge expired before now-grace, and
// returns the number deleted. It first takes a transaction-scoped advisory lock; if
// another sweep already holds it, Sweep returns (0, nil) without scanning, so
// duplicate triggers (multiple schedulers, retries, topology churn) are harmless.
func (s *Sweeper) Sweep(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-s.grace)

	var deleted int64
	err := s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		var locked bool
		if err := q.QueryRow(ctx, tryLockSQL, pendingAccountGCLockKey).Scan(&locked); err != nil {
			return fmt.Errorf("accountgc: acquire advisory lock: %w", err)
		}
		if !locked {
			return nil
		}
		tag, err := q.Exec(ctx, sweepSQL, cutoff)
		if err != nil {
			return fmt.Errorf("accountgc: delete pending accounts: %w", err)
		}
		deleted = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

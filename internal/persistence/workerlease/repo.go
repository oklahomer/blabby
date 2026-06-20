// Package workerlease manages Snowflake worker-id leases in the worker_lease
// table. Every id-minting process acquires a worker id with a fencing token at
// startup, renews it on a heartbeat, and releases it on shutdown; the fencing
// token is what makes a stale holder's renewal fail so two live processes never
// share a worker id (and therefore never mint duplicate ids).
//
// The repo issues raw parameterized SQL: its three statements are fixed, and the
// acquire is a single set-based statement that a query builder would only
// obscure. lease_token is read and written as text (`$n::uuid` / `::text`) so the
// package needs no uuid codec registration on the pool.
package workerlease

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// ErrNoCapacity is returned by Acquire when every worker id in [0, 1023] is held
// by an unexpired lease.
var ErrNoCapacity = errors.New("workerlease: no free worker id")

// acquireAttempts bounds the retries when a concurrent acquirer wins the slot this
// process picked (an empty result despite free capacity). A genuinely full table
// keeps returning no rows across all attempts and surfaces as ErrNoCapacity.
const acquireAttempts = 8

// Lease is a held worker-id lease: the claimed id and the fencing token that gates
// its renewal and release.
type Lease struct {
	WorkerID int
	Token    uuid.UUID
}

// Repo runs worker_lease operations against a postgres.Querier (the pool).
type Repo struct {
	q postgres.Querier
}

// NewRepo returns a Repo backed by q.
func NewRepo(q postgres.Querier) *Repo {
	return &Repo{q: q}
}

// acquireSQL claims the lowest worker id in [0, 1023] that has no unexpired lease
// (absent or expired), writing a fresh token and a now+ttl expiry. The INSERT
// covers an absent row; the ON CONFLICT … WHERE expires_at <= now() covers an
// expired one. A concurrent acquirer that just claimed the same slot leaves its
// expiry in the future, so the conflicting UPDATE matches no row and this process
// gets an empty result — the caller retries.
const acquireSQL = `
INSERT INTO worker_lease (worker_id, owner, lease_token, acquired_at, renewed_at, expires_at)
SELECT gs, $1, $2::uuid, now(), now(), now() + make_interval(secs => $3)
FROM generate_series(0, 1023) AS gs
WHERE NOT EXISTS (
    SELECT 1 FROM worker_lease wl WHERE wl.worker_id = gs AND wl.expires_at > now()
)
ORDER BY gs
LIMIT 1
ON CONFLICT (worker_id) DO UPDATE
SET owner = EXCLUDED.owner,
    lease_token = EXCLUDED.lease_token,
    acquired_at = EXCLUDED.acquired_at,
    renewed_at = EXCLUDED.renewed_at,
    expires_at = EXCLUDED.expires_at
WHERE worker_lease.expires_at <= now()
RETURNING worker_id`

// Acquire claims a free or expired worker id under owner, with the lease valid for
// ttl. It returns ErrNoCapacity if no id is free after the bounded retries.
func (r *Repo) Acquire(ctx context.Context, owner string, ttl time.Duration) (Lease, error) {
	ttlSecs := ttl.Seconds()
	for attempt := 0; attempt < acquireAttempts; attempt++ {
		token, err := uuid.NewRandom()
		if err != nil {
			return Lease{}, fmt.Errorf("workerlease: generate token: %w", err)
		}
		var workerID int
		err = r.q.QueryRow(ctx, acquireSQL, owner, token.String(), ttlSecs).Scan(&workerID)
		switch {
		case err == nil:
			// Both write paths set lease_token to the token we just passed ($2), so
			// the returned row is ours — no need to read the token back and parse it.
			return Lease{WorkerID: workerID, Token: token}, nil
		case errors.Is(err, pgx.ErrNoRows):
			continue // slot lost to a concurrent acquirer, or table full; retry
		default:
			return Lease{}, fmt.Errorf("workerlease: acquire: %w", err)
		}
	}
	return Lease{}, ErrNoCapacity
}

// renewSQL extends the lease only while this process still holds it: the token
// must match and the lease must not have already expired. A row count of zero
// means the lease was lost (token rotated by a new holder, or expiry elapsed).
const renewSQL = `
UPDATE worker_lease
SET renewed_at = now(), expires_at = now() + make_interval(secs => $3)
WHERE worker_id = $1 AND lease_token = $2::uuid AND expires_at > now()`

// Renew conditionally extends lease for another ttl. It reports held=false (with a
// nil error) when the conditional update matched no row, i.e. the lease was lost —
// the fencing signal the caller must treat as "stop minting".
func (r *Repo) Renew(ctx context.Context, lease Lease, ttl time.Duration) (held bool, err error) {
	tag, err := r.q.Exec(ctx, renewSQL, lease.WorkerID, lease.Token.String(), ttl.Seconds())
	if err != nil {
		return false, fmt.Errorf("workerlease: renew: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// releaseSQL drops the lease so its worker id is immediately reclaimable. It is
// token-scoped so a stale holder cannot delete a lease another process now owns.
const releaseSQL = `DELETE FROM worker_lease WHERE worker_id = $1 AND lease_token = $2::uuid`

// Release frees lease on shutdown. It is best-effort: a deleted-zero-rows outcome
// (the lease was already lost) is not an error.
func (r *Repo) Release(ctx context.Context, lease Lease) error {
	if _, err := r.q.Exec(ctx, releaseSQL, lease.WorkerID, lease.Token.String()); err != nil {
		return fmt.Errorf("workerlease: release: %w", err)
	}
	return nil
}

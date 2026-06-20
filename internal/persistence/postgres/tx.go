package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the subset of pgx that repositories use. Both *pgxpool.Pool and
// pgx.Tx satisfy it, so a repository method runs identically against the pool
// (each statement autocommitting) or inside a transaction the caller opened with
// [Transactor.WithinTx] — the grain passes whichever it holds.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Compile-time proof that the two pgx types repositories receive both satisfy
// Querier; if a future pgx version changes a signature this stops compiling here
// rather than at every call site.
var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = pgx.Tx(nil)
)

// Transactor owns the transaction boundary so a caller can compose several
// repository writes into one atomic unit — e.g. the Room grain writing a
// room_membership change and its derived journal event together. The transaction
// is grain-local (the grain is the single writer for a room), so no cross-grain
// coordination is involved.
type Transactor struct {
	pool *pgxpool.Pool
}

// NewTransactor returns a Transactor that opens transactions on pool.
func NewTransactor(pool *pgxpool.Pool) *Transactor {
	return &Transactor{pool: pool}
}

// WithinTx begins a transaction, calls fn with it, and commits if fn returns nil.
// If fn returns an error the transaction is rolled back and the error propagates;
// the same rollback runs if fn panics (the deferred rollback executes as the panic
// unwinds), so a panicking caller never leaks an open transaction or its
// connection. The Querier passed to fn must not be retained past fn's return.
func (t *Transactor) WithinTx(ctx context.Context, fn func(q Querier) error) error {
	tx, err := t.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// Rollback is a no-op once Commit has succeeded (it returns pgx.ErrTxClosed,
	// which we ignore), so running it on every path — including a panic unwinding
	// through this defer — guarantees the connection is released.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

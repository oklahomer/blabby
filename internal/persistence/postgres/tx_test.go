package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// TestWithinTxIntegration verifies the commit and rollback paths against a real
// database. It is skipped unless BLABBY_DATABASE_URL points at a reachable
// PostgreSQL instance (e.g. `make up`), keeping the default `go test` hermetic.
func TestWithinTxIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(envDSNKey))
	if dsn == "" {
		t.Skipf("%s not set; skipping database integration test", envDSNKey)
	}

	cfg, err := newConfig(dsn, defaultMaxConns, defaultMaxConnIdleTime, defaultMaxConnLifetime)
	if err != nil {
		t.Fatalf("newConfig: %v", err)
	}
	ctx := context.Background()
	pool, err := NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	// Registered before the table cleanup below so it runs after it (LIFO); a
	// defer would close the pool before any t.Cleanup could use it.
	t.Cleanup(pool.Close)

	const table = "_tx_within_test"
	if _, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (n int)"); err != nil {
		t.Fatalf("create scratch table: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table); err != nil {
			t.Errorf("cleanup: drop scratch table: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, "TRUNCATE "+table); err != nil {
		t.Fatalf("truncate scratch table: %v", err)
	}

	tx := NewTransactor(pool)

	// Commit path: a nil return commits the insert.
	if err := tx.WithinTx(ctx, func(q Querier) error {
		_, err := q.Exec(ctx, "INSERT INTO "+table+" (n) VALUES (1)")
		return err
	}); err != nil {
		t.Fatalf("commit WithinTx: %v", err)
	}
	if got := scratchCount(t, pool, table); got != 1 {
		t.Fatalf("after commit, count = %d, want 1", got)
	}

	// Rollback path: a returned error rolls the insert back and propagates.
	sentinel := errors.New("boom")
	err = tx.WithinTx(ctx, func(q Querier) error {
		if _, err := q.Exec(ctx, "INSERT INTO "+table+" (n) VALUES (2)"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback WithinTx error = %v, want sentinel", err)
	}
	if got := scratchCount(t, pool, table); got != 1 {
		t.Fatalf("after rollback, count = %d, want 1 (the second insert must not persist)", got)
	}
}

func scratchCount(t *testing.T, q Querier, table string) int {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count(%s): %v", table, err)
	}
	return n
}

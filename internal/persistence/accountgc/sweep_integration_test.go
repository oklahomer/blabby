package accountgc

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// TestSweepIntegration exercises the real DELETE + cascade + status/cutoff filters
// against PostgreSQL. Skipped unless BLABBY_DATABASE_URL points at a reachable
// database with the schema applied. It cleans up the rows it creates.
func TestSweepIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("BLABBY_DATABASE_URL"))
	if dsn == "" {
		t.Skip("BLABBY_DATABASE_URL not set; skipping database integration test")
	}
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, postgres.Config{
		DSN: dsn, MaxConns: 4, MaxConnIdleTime: time.Minute, MaxConnLifetime: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	// Registered before the row cleanup below so it runs after it (LIFO); a
	// defer would close the pool before any t.Cleanup could use it — leaking
	// this test's rows, whose expiring challenges then inflate the next run's
	// global deleted count.
	t.Cleanup(pool.Close)

	base := time.Now().UnixNano()
	now := time.Now()
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM service_user WHERE id > $1 AND id <= $2", base, base+10); err != nil {
			t.Errorf("cleanup: delete seeded accounts: %v", err)
		}
	})

	const (
		expiredPending = iota + 1 // pending, challenge long expired -> swept
		recentPending             // pending, challenge still live    -> kept
		expiredActive             // active, challenge expired         -> kept (not pending)
	)
	seedAccount(t, pool, base+expiredPending, "pending", now.Add(-2*time.Hour))
	seedAccount(t, pool, base+recentPending, "pending", now.Add(10*time.Minute))
	seedAccount(t, pool, base+expiredActive, "active", now.Add(-2*time.Hour))

	sweeper, err := NewSweeper(postgres.NewTransactor(pool), time.Hour)
	if err != nil {
		t.Fatalf("NewSweeper: %v", err)
	}
	deleted, err := sweeper.Sweep(ctx, now)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (only the expired pending account)", deleted)
	}

	if userExists(t, pool, base+expiredPending) {
		t.Error("expired pending account should have been swept")
	}
	if challengeExists(t, pool, base+expiredPending) {
		t.Error("swept account's verification challenge should have cascaded away")
	}
	if !userExists(t, pool, base+recentPending) {
		t.Error("pending account with a live challenge must be kept")
	}
	if !userExists(t, pool, base+expiredActive) {
		t.Error("active account must be kept regardless of challenge expiry")
	}
}

func seedAccount(t *testing.T, pool *pgxpool.Pool, id int64, status string, expiresAt time.Time) {
	t.Helper()
	handle := fmt.Sprintf("gct_%d", id)
	publicCode := fmt.Sprintf("G%09d", id%1_000_000_000)
	_, err := pool.Exec(context.Background(), `
INSERT INTO service_user (id, public_code, mail_address, handle, handle_norm, display_name, password_hash, status)
VALUES ($1, $2, $3, $4, $4, $4, $5, $6::user_status)`,
		id, publicCode, fmt.Sprintf("gct-%d@example.com", id), handle, []byte("x"), status)
	if err != nil {
		t.Fatalf("seed service_user %d: %v", id, err)
	}
	_, err = pool.Exec(context.Background(),
		`INSERT INTO email_verification (user_id, pin_hash, expires_at) VALUES ($1, $2, $3)`,
		id, []byte("x"), expiresAt)
	if err != nil {
		t.Fatalf("seed email_verification %d: %v", id, err)
	}
}

func userExists(t *testing.T, pool *pgxpool.Pool, id int64) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM service_user WHERE id = $1)", id).Scan(&exists); err != nil {
		t.Fatalf("userExists %d: %v", id, err)
	}
	return exists
}

func challengeExists(t *testing.T, pool *pgxpool.Pool, id int64) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM email_verification WHERE user_id = $1)", id).Scan(&exists); err != nil {
		t.Fatalf("challengeExists %d: %v", id, err)
	}
	return exists
}

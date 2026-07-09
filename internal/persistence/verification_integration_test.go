package persistence

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// TestVerifyRepoIntegration exercises the repo against a real database. Skipped
// unless BLABBY_DATABASE_URL points at a reachable PostgreSQL instance (e.g.
// `make up`) with the schema and dev seed applied. It uses seed user 1 (alice)
// only as a satisfied foreign key — a verification row is meaningless for an
// active account, but it lets the CRUD path run against the real table and FK —
// and deletes the row it created, so it is re-runnable.
func TestVerifyRepoIntegration(t *testing.T) {
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
	// defer would close the pool before any t.Cleanup could use it.
	t.Cleanup(pool.Close)

	userID := mustUserID(t, 1) // seed user alice, used only as a satisfied FK
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM email_verification WHERE user_id = 1"); err != nil {
			t.Errorf("cleanup: delete verification row: %v", err)
		}
	})
	// Start from a clean slate in case a prior aborted run left a row.
	if _, err := pool.Exec(ctx, "DELETE FROM email_verification WHERE user_id = 1"); err != nil {
		t.Fatalf("pre-clean verification row: %v", err)
	}

	repo := NewVerificationRepo()
	expiry := time.Now().Add(10 * time.Minute).Truncate(time.Microsecond)
	sentAt := time.Now().Truncate(time.Microsecond)
	if err := repo.Create(ctx, pool, VerificationCreateParams{
		UserID: userID, PinHash: []byte("$2a$12$placeholder.pin.hash"), ExpiresAt: expiry, SentAt: sentAt,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	v, err := repo.FindByUser(ctx, pool, userID)
	if err != nil {
		t.Fatalf("FindByUser: %v", err)
	}
	if v.Attempts != 0 || v.ResendCount != 0 {
		t.Fatalf("fresh challenge = attempts %d / resend %d, want 0 / 0", v.Attempts, v.ResendCount)
	}
	if v.LastSentAt.IsZero() {
		t.Fatalf("LastSentAt is zero; Create must record the initial send time")
	}

	// Two failed attempts bump the counter monotonically.
	for want := 1; want <= 2; want++ {
		got, err := repo.IncrementAttempts(ctx, pool, userID)
		if err != nil {
			t.Fatalf("IncrementAttempts: %v", err)
		}
		if got != want {
			t.Fatalf("IncrementAttempts = %d, want %d", got, want)
		}
	}

	// Resend installs a fresh PIN, clears the lock, and bumps resend_count.
	newExpiry := time.Now().Add(20 * time.Minute).Truncate(time.Microsecond)
	resendAt := time.Now().Truncate(time.Microsecond)
	if err := repo.Resend(ctx, pool, VerificationResendParams{
		UserID: userID, PinHash: []byte("$2a$12$fresh.pin.hash"), ExpiresAt: newExpiry, SentAt: resendAt,
	}, VerificationResendPolicy{PreviousSentBefore: sentAt, MaxResendCount: 5}); err != nil {
		t.Fatalf("Resend: %v", err)
	}
	after, err := repo.FindByUser(ctx, pool, userID)
	if err != nil {
		t.Fatalf("FindByUser after resend: %v", err)
	}
	if after.Attempts != 0 || after.ResendCount != 1 {
		t.Fatalf("after resend = attempts %d / resend %d, want 0 / 1", after.Attempts, after.ResendCount)
	}
	if !after.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("after resend expiry = %v, want %v", after.ExpiresAt, newExpiry)
	}
	if err := repo.Resend(ctx, pool, VerificationResendParams{
		UserID: userID, PinHash: []byte("$2a$12$blocked.pin.hash"), ExpiresAt: newExpiry, SentAt: time.Now().Truncate(time.Microsecond),
	}, VerificationResendPolicy{PreviousSentBefore: resendAt.Add(-time.Second), MaxResendCount: 5}); !errors.Is(err, ErrVerificationRateLimited) {
		t.Fatalf("Resend(rate limited) = %v, want ErrVerificationRateLimited", err)
	}

	// Delete removes the challenge; a second find reports not found.
	if err := repo.Delete(ctx, pool, userID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.FindByUser(ctx, pool, userID); !errors.Is(err, ErrVerificationNotFound) {
		t.Fatalf("FindByUser after delete = %v, want ErrVerificationNotFound", err)
	}
	if err := repo.Delete(ctx, pool, userID); !errors.Is(err, ErrVerificationNotFound) {
		t.Fatalf("Delete(missing) = %v, want ErrVerificationNotFound", err)
	}
}

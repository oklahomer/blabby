package workerlease

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// fakeQuerier is an in-memory postgres.Querier for exercising the repo's control
// flow without a database.
type fakeQuerier struct {
	queryRow func(args ...any) pgx.Row
	execTag  pgconn.CommandTag
	execErr  error
}

var _ postgres.Querier = (*fakeQuerier)(nil)

func (f *fakeQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return f.execTag, f.execErr
}

func (f *fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("workerlease test: Query not used")
}

func (f *fakeQuerier) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	return f.queryRow(args...)
}

type fakeRow struct{ scan func(dest ...any) error }

func (r fakeRow) Scan(dest ...any) error { return r.scan(dest...) }

func TestAcquireReturnsClaimedLease(t *testing.T) {
	wantID := 3
	calls := 0
	var sentToken string
	fq := &fakeQuerier{queryRow: func(args ...any) pgx.Row {
		calls++
		sentToken = args[1].(string) // $1 owner, $2 token
		return fakeRow{scan: func(dest ...any) error {
			*(dest[0].(*int)) = wantID
			return nil
		}}
	}}

	lease, err := NewRepo(fq).Acquire(context.Background(), "p", time.Hour)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if lease.WorkerID != wantID {
		t.Fatalf("WorkerID = %d, want %d", lease.WorkerID, wantID)
	}
	// Acquire returns the token it generated and sent, not a value read back.
	if lease.Token.String() != sentToken {
		t.Fatalf("Token = %s, want the sent token %s", lease.Token, sentToken)
	}
	if calls != 1 {
		t.Fatalf("queried %d times, want 1 (no retry on success)", calls)
	}
}

func TestAcquireRetriesThenReturnsNoCapacity(t *testing.T) {
	calls := 0
	fq := &fakeQuerier{queryRow: func(...any) pgx.Row {
		calls++
		return fakeRow{scan: func(...any) error { return pgx.ErrNoRows }}
	}}

	_, err := NewRepo(fq).Acquire(context.Background(), "p", time.Hour)
	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("Acquire: got %v, want ErrNoCapacity", err)
	}
	if calls != acquireAttempts {
		t.Fatalf("queried %d times, want %d (one per attempt)", calls, acquireAttempts)
	}
}

func TestAcquirePropagatesHardErrorWithoutRetrying(t *testing.T) {
	sentinel := errors.New("db down")
	calls := 0
	fq := &fakeQuerier{queryRow: func(...any) pgx.Row {
		calls++
		return fakeRow{scan: func(...any) error { return sentinel }}
	}}

	_, err := NewRepo(fq).Acquire(context.Background(), "p", time.Hour)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Acquire: got %v, want the db error", err)
	}
	if errors.Is(err, ErrNoCapacity) {
		t.Fatal("a hard error must not be reported as ErrNoCapacity")
	}
	if calls != 1 {
		t.Fatalf("queried %d times, want 1 (no retry on a hard error)", calls)
	}
}

func TestRenewReportsHeld(t *testing.T) {
	tests := []struct {
		name     string
		tag      string
		wantHeld bool
	}{
		{name: "one row updated means still held", tag: "UPDATE 1", wantHeld: true},
		{name: "zero rows means the lease was lost", tag: "UPDATE 0", wantHeld: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fq := &fakeQuerier{execTag: pgconn.NewCommandTag(tc.tag)}
			held, err := NewRepo(fq).Renew(context.Background(), Lease{WorkerID: 1, Token: uuid.New()}, time.Hour)
			if err != nil {
				t.Fatalf("Renew: %v", err)
			}
			if held != tc.wantHeld {
				t.Fatalf("held = %v, want %v", held, tc.wantHeld)
			}
		})
	}
}

func TestRenewPropagatesError(t *testing.T) {
	sentinel := errors.New("db down")
	_, err := NewRepo(&fakeQuerier{execErr: sentinel}).Renew(context.Background(), Lease{WorkerID: 1, Token: uuid.New()}, time.Hour)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Renew: got %v, want the db error", err)
	}
}

func TestReleasePropagatesError(t *testing.T) {
	sentinel := errors.New("db down")
	err := NewRepo(&fakeQuerier{execErr: sentinel}).Release(context.Background(), Lease{WorkerID: 1, Token: uuid.New()})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Release: got %v, want the db error", err)
	}
}

// TestWorkerLeaseRepoIntegration exercises the fencing semantics against a real
// database. Skipped unless BLABBY_DATABASE_URL points at a reachable PostgreSQL
// instance (e.g. `make up`). It truncates worker_lease so the lowest-id-first
// assignment is deterministic.
func TestWorkerLeaseRepoIntegration(t *testing.T) {
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
	// Registered before the truncate cleanup below so it runs after it (LIFO);
	// a defer would close the pool before any t.Cleanup could use it.
	t.Cleanup(pool.Close)

	mustExec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	mustExec("TRUNCATE worker_lease")
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "TRUNCATE worker_lease"); err != nil {
			t.Errorf("cleanup: truncate worker_lease: %v", err)
		}
	})

	repo := NewRepo(pool)

	// A clean table assigns the lowest ids first, and they are distinct.
	l0, err := repo.Acquire(ctx, "p0", time.Hour)
	if err != nil {
		t.Fatalf("acquire l0: %v", err)
	}
	l1, err := repo.Acquire(ctx, "p1", time.Hour)
	if err != nil {
		t.Fatalf("acquire l1: %v", err)
	}
	if l0.WorkerID != 0 || l1.WorkerID != 1 {
		t.Fatalf("worker ids = %d, %d; want 0, 1 (lowest-first and distinct)", l0.WorkerID, l1.WorkerID)
	}

	// A held lease renews.
	if held, err := repo.Renew(ctx, l0, time.Hour); err != nil || !held {
		t.Fatalf("renew l0: held=%v err=%v; want held=true", held, err)
	}

	// Fencing: a stale token cannot renew a lease another process holds.
	stale := Lease{WorkerID: l1.WorkerID, Token: uuid.New()}
	if held, err := repo.Renew(ctx, stale, time.Hour); err != nil || held {
		t.Fatalf("stale-token renew: held=%v err=%v; want held=false", held, err)
	}

	// An expired lease cannot renew and is reclaimable with a rotated token.
	mustExec("UPDATE worker_lease SET expires_at = now() - interval '1 hour' WHERE worker_id = 0")
	if held, err := repo.Renew(ctx, l0, time.Hour); err != nil || held {
		t.Fatalf("renew of expired lease: held=%v err=%v; want held=false", held, err)
	}
	reclaim, err := repo.Acquire(ctx, "p2", time.Hour)
	if err != nil {
		t.Fatalf("reclaim acquire: %v", err)
	}
	if reclaim.WorkerID != 0 {
		t.Fatalf("reclaimed worker id = %d, want 0 (the freed slot)", reclaim.WorkerID)
	}
	if reclaim.Token == l0.Token {
		t.Fatal("reclaim must rotate the fencing token")
	}

	// Release frees the id (its row is gone).
	if err := repo.Release(ctx, l1); err != nil {
		t.Fatalf("release l1: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM worker_lease WHERE worker_id = 1").Scan(&count); err != nil {
		t.Fatalf("count after release: %v", err)
	}
	if count != 0 {
		t.Fatalf("worker_lease still has a row for id 1 after release")
	}
}

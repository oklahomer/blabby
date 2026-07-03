package accountgc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

type fakeQuerier struct {
	locked       bool
	lockErr      error
	execTag      pgconn.CommandTag
	execErr      error
	execCalls    int
	lastExecArgs []any
}

var _ postgres.Querier = (*fakeQuerier)(nil)

func (f *fakeQuerier) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return fakeRow{locked: f.locked, err: f.lockErr}
}

func (f *fakeQuerier) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls++
	f.lastExecArgs = args
	return f.execTag, f.execErr
}

func (f *fakeQuerier) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

type fakeRow struct {
	locked bool
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*bool)) = r.locked
	return nil
}

type fakeTx struct {
	q     *fakeQuerier
	calls int
}

func (t *fakeTx) WithinTx(_ context.Context, fn func(q postgres.Querier) error) error {
	t.calls++
	return fn(t.q)
}

func newSweeperForTest(t *testing.T, tx Transactor, grace time.Duration) *Sweeper {
	t.Helper()
	sweeper, err := NewSweeper(tx, grace)
	if err != nil {
		t.Fatalf("NewSweeper: %v", err)
	}
	return sweeper
}

func TestNewSweeperRejectsNegativeGrace(t *testing.T) {
	if _, err := NewSweeper(&fakeTx{q: &fakeQuerier{}}, -time.Nanosecond); !errors.Is(err, ErrInvalidGrace) {
		t.Fatalf("NewSweeper err = %v, want ErrInvalidGrace", err)
	}
}

func TestSweep_DeletesWhenLockAcquired(t *testing.T) {
	q := &fakeQuerier{locked: true, execTag: pgconn.NewCommandTag("DELETE 3")}
	now := time.Unix(1_000_000, 0).UTC()

	deleted, err := newSweeperForTest(t, &fakeTx{q: q}, time.Hour).Sweep(context.Background(), now)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted = %d, want 3", deleted)
	}
	if q.execCalls != 1 {
		t.Fatalf("delete executed %d times, want 1", q.execCalls)
	}
	if got := q.lastExecArgs[0].(time.Time); !got.Equal(now.Add(-time.Hour)) {
		t.Fatalf("cutoff = %v, want now-grace %v", got, now.Add(-time.Hour))
	}
}

func TestSweep_NoOpWhenLockNotAcquired(t *testing.T) {
	q := &fakeQuerier{locked: false}

	deleted, err := newSweeperForTest(t, &fakeTx{q: q}, time.Hour).Sweep(context.Background(), time.Unix(1_000_000, 0).UTC())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0", deleted)
	}
	if q.execCalls != 0 {
		t.Fatalf("delete executed %d times, want 0 when the lock is held elsewhere", q.execCalls)
	}
}

func TestSweep_PropagatesErrors(t *testing.T) {
	now := time.Unix(1_000_000, 0).UTC()

	t.Run("lock query fails", func(t *testing.T) {
		q := &fakeQuerier{lockErr: errors.New("boom")}
		if _, err := newSweeperForTest(t, &fakeTx{q: q}, time.Hour).Sweep(context.Background(), now); err == nil {
			t.Fatal("Sweep: want an error when the lock query fails")
		}
	})
	t.Run("delete fails", func(t *testing.T) {
		q := &fakeQuerier{locked: true, execErr: errors.New("boom")}
		if _, err := newSweeperForTest(t, &fakeTx{q: q}, time.Hour).Sweep(context.Background(), now); err == nil {
			t.Fatal("Sweep: want an error when the delete fails")
		}
	})
}

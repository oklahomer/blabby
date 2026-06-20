package workerlease

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/snowflake"
)

// fakeClock is a controllable clock shared by the manager and its generator.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeStore is an in-memory LeaseStore whose behavior the test mutates between
// calls. It hands out increasing worker ids so a re-acquire is observably distinct.
type fakeStore struct {
	mu           sync.Mutex
	nextID       int
	renewHeld    bool
	renewErr     error
	acquireErr   error
	acquireCalls int
	released     bool
}

var _ LeaseStore = (*fakeStore)(nil)

func (f *fakeStore) Acquire(context.Context, string, time.Duration) (Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquireCalls++
	if f.acquireErr != nil {
		return Lease{}, f.acquireErr
	}
	id := f.nextID
	f.nextID++
	return Lease{WorkerID: id}, nil
}

func (f *fakeStore) Renew(context.Context, Lease, time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renewErr != nil {
		return false, f.renewErr
	}
	return f.renewHeld, nil
}

func (f *fakeStore) Release(context.Context, Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = true
	return nil
}

func (f *fakeStore) set(fn func(*fakeStore)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeStore) acquires() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquireCalls
}

// newTestManager builds a manager wired to a fake clock starting well after the
// epoch (so ids stay positive) with a 30s ttl / 5s margin (deadline = send + 25s).
//
// Tests built on it call Start and then drive the lease loop by calling tick
// directly. That stays deterministic because the real heartbeat uses the default
// 10s interval, which never fires within these sub-second tests — so the manual
// tick is the only one.
func newTestManager(t *testing.T, store LeaseStore, clk *fakeClock) *Manager {
	t.Helper()
	clk.t = snowflake.Epoch().Add(24 * time.Hour)
	m, err := NewManager(store, "test",
		WithTTL(30*time.Second),
		WithMargin(5*time.Second),
		WithClock(clk.now),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestNewManagerCadenceValidation(t *testing.T) {
	if _, err := NewManager(&fakeStore{}, "test"); err != nil {
		t.Fatalf("default cadence should be valid: %v", err)
	}

	invalid := []struct {
		name string
		opts []ManagerOption
	}{
		{"zero ttl", []ManagerOption{WithTTL(0)}},
		{"negative margin", []ManagerOption{WithMargin(-time.Second)}},
		{"ttl not exceeding margin", []ManagerOption{WithTTL(5 * time.Second), WithMargin(5 * time.Second)}},
		{"zero renew interval", []ManagerOption{WithRenewInterval(0)}},
		{"renew interval not shorter than ttl-margin", []ManagerOption{
			WithTTL(30 * time.Second), WithMargin(5 * time.Second), WithRenewInterval(25 * time.Second),
		}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewManager(&fakeStore{}, "test", tc.opts...); err == nil {
				t.Fatal("expected a cadence error, got nil")
			}
		})
	}
}

func TestNextBeforeStartFailsClosed(t *testing.T) {
	m, err := NewManager(&fakeStore{}, "test")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Next(); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Next before Start: got %v, want ErrNotStarted", err)
	}
}

func TestMintingIsBoundedByTheMonotonicDeadline(t *testing.T) {
	clk := &fakeClock{}
	m := newTestManager(t, &fakeStore{renewHeld: true}, clk)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	if _, err := m.Next(); err != nil {
		t.Fatalf("Next within lease: %v", err)
	}
	// Reach the deadline (send + 25s) without any renewal: minting must stop. The
	// generator is gated purely on this local deadline, not on a DB expiry, so this
	// holds regardless of any wall-clock movement.
	clk.advance(25 * time.Second)
	if _, err := m.Next(); !errors.Is(err, snowflake.ErrLeaseExpired) {
		t.Fatalf("Next at deadline: got %v, want ErrLeaseExpired", err)
	}
}

func TestRenewExtendsTheDeadline(t *testing.T) {
	clk := &fakeClock{}
	m := newTestManager(t, &fakeStore{renewHeld: true}, clk)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	clk.advance(20 * time.Second) // still within the initial deadline (25s)
	m.tick(context.Background())  // renew → new deadline = now + 25s = 45s

	clk.advance(20 * time.Second) // now 40s: past the old 25s deadline, within the new 45s
	if _, err := m.Next(); err != nil {
		t.Fatalf("Next after renew should still mint, got %v", err)
	}
}

func TestTransientRenewErrorDoesNotExtendTheDeadline(t *testing.T) {
	clk := &fakeClock{}
	store := &fakeStore{renewHeld: true}
	m := newTestManager(t, store, clk)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	// A transient renew error is logged and leaves the deadline untouched.
	store.set(func(s *fakeStore) { s.renewErr = errors.New("db blip") })
	clk.advance(10 * time.Second) // now 10s, still within the 25s deadline
	m.tick(context.Background())
	if _, err := m.Next(); err != nil {
		t.Fatalf("within the deadline after a transient error, Next should mint: %v", err)
	}

	// The deadline was not extended, so minting halts when the original one lapses.
	clk.advance(15 * time.Second) // now 25s
	if _, err := m.Next(); !errors.Is(err, snowflake.ErrLeaseExpired) {
		t.Fatalf("a transient error must not extend the deadline: got %v, want ErrLeaseExpired", err)
	}
}

func TestLeaseLossHaltsThenReacquires(t *testing.T) {
	clk := &fakeClock{}
	store := &fakeStore{renewHeld: true}
	m := newTestManager(t, store, clk)
	if err := m.Start(context.Background()); err != nil { // acquire #1 (worker 0)
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()
	if _, err := m.Next(); err != nil {
		t.Fatalf("Next while held: %v", err)
	}

	// Lease lost AND no capacity to re-acquire → minting halts.
	store.set(func(s *fakeStore) { s.renewHeld = false; s.acquireErr = ErrNoCapacity })
	m.tick(context.Background())
	if _, err := m.Next(); !errors.Is(err, snowflake.ErrLeaseExpired) {
		t.Fatalf("after loss without capacity, Next = %v, want ErrLeaseExpired", err)
	}

	// Capacity returns → the next beat re-acquires and minting resumes.
	store.set(func(s *fakeStore) { s.acquireErr = nil })
	m.tick(context.Background())
	if _, err := m.Next(); err != nil {
		t.Fatalf("after re-acquire, Next should mint, got %v", err)
	}
	if got := store.acquires(); got != 3 {
		t.Fatalf("acquire calls = %d, want 3 (initial + failed re-acquire + successful re-acquire)", got)
	}
}

func TestStartStopReleasesTheLease(t *testing.T) {
	clk := &fakeClock{}
	clk.t = snowflake.Epoch().Add(24 * time.Hour)
	store := &fakeStore{renewHeld: true}
	// Default cadence is valid; the 10s heartbeat never fires during this sub-second
	// test, so the store is touched only by Start's acquire and Stop's release.
	m, err := NewManager(store, "test", WithClock(clk.now))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Next(); err != nil {
		t.Fatalf("Next after Start: %v", err)
	}
	m.Stop()
	if !store.released {
		t.Fatal("Stop did not release the lease")
	}
}

func TestStartReleasesLeaseWhenGeneratorRejectsWorkerID(t *testing.T) {
	clk := &fakeClock{}
	clk.t = snowflake.Epoch().Add(24 * time.Hour)
	store := &fakeStore{nextID: snowflake.MaxWorkerID + 1}
	m, err := NewManager(store, "test", WithClock(clk.now))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if err := m.Start(context.Background()); err == nil {
		t.Fatal("Start with out-of-range worker id: expected error, got nil")
	}
	if !store.released {
		t.Fatal("Start did not release the acquired lease after generator construction failed")
	}
}

func TestStartTwiceReturnsAlreadyStarted(t *testing.T) {
	clk := &fakeClock{}
	clk.t = snowflake.Epoch().Add(24 * time.Hour)
	store := &fakeStore{renewHeld: true}
	m, err := NewManager(store, "test", WithClock(clk.now))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer m.Stop()

	if err := m.Start(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Start: got %v, want ErrAlreadyStarted", err)
	}
	if got := store.acquires(); got != 1 {
		t.Fatalf("acquire calls = %d, want 1 (the rejected Start must not acquire again)", got)
	}
}

package workerlease

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklahomer/blabby/internal/snowflake"
)

// Lease cadence defaults. renewInterval must stay below ttl−margin so a renewal is
// attempted (and, on failure, retried) before the local deadline lapses.
const (
	defaultTTL           = 30 * time.Second
	defaultMargin        = 5 * time.Second
	defaultRenewInterval = 10 * time.Second

	// defaultBeatTimeout bounds the database work of a single renewal beat. It caps
	// how long the loop can sit inside a hung store call, which in turn caps how long
	// Stop waits for an in-flight beat to observe the stop signal.
	defaultBeatTimeout = 5 * time.Second
	// defaultReleaseTimeout bounds the best-effort lease release on shutdown. Without
	// it a hung release would keep the loop's teardown from completing, blocking Stop
	// (which waits for the loop to finish) indefinitely.
	defaultReleaseTimeout = 5 * time.Second
)

// Sentinel errors from the Manager lifecycle.
var (
	// ErrNotStarted is returned by Next before Start has acquired the first lease.
	ErrNotStarted = errors.New("workerlease: manager not started")
	// ErrAlreadyStarted is returned by a second Start on a running Manager.
	ErrAlreadyStarted = errors.New("workerlease: manager already started")
)

// LeaseStore is the subset of the worker_lease repository the Manager drives.
// *Repo satisfies it; tests substitute a fake.
type LeaseStore interface {
	Acquire(ctx context.Context, owner string, ttl time.Duration) (Lease, error)
	Renew(ctx context.Context, lease Lease, ttl time.Duration) (bool, error)
	Release(ctx context.Context, lease Lease) error
}

// Manager keeps a worker-id lease alive and exposes a fenced Snowflake id source.
// It acquires a lease at Start, renews it on a heartbeat, feeds the generator a
// local monotonic deadline derived from each successful renewal, and on a lost
// lease halts minting and re-acquires a fresh worker id.
//
// The deadline is computed from the injected clock (monotonic in production) at the
// instant each acquire/renew is *sent*, never from the lease's DB expiry — so an
// NTP step or host↔DB skew can never extend minting past the lease.
//
// A single loop goroutine owns the renewal ticker, the current lease/generator, and
// teardown. It publishes the live session through cur so Next reads it lock-free.
// Start publishes the initial session before launching the loop; after that the loop
// is cur's sole writer (replacing it on re-acquire, clearing it on exit), so cur
// needs no mutex. Stop only signals the loop and waits, never touching the session.
type Manager struct {
	store         LeaseStore
	owner         string
	ttl           time.Duration
	margin        time.Duration
	renewInterval time.Duration
	now           func() time.Time
	// beatTimeout bounds each renewal beat's store call; releaseTimeout bounds the
	// shutdown release. Both keep a hung store call from stalling the loop's teardown
	// and, in turn, Stop.
	beatTimeout    time.Duration
	releaseTimeout time.Duration

	// lifecycleMu serializes Start and Stop against each other.
	lifecycleMu sync.Mutex
	// cur holds the live lease/generator, or nil when not running. It is the
	// started marker (Start rejects a non-nil cur) and Next's lock-free source.
	// Start publishes the first session; after that the owning loop is the sole
	// writer, and a later Start republishes only after the prior loop cleared cur.
	cur atomic.Pointer[session]
	// stop and done are the current run goroutine's control channels, recorded so
	// Stop can signal it and wait for it. The loop receives its own copies as
	// arguments, so a restart can replace these fields without disturbing an
	// already-exiting goroutine.
	stop chan struct{}
	done chan struct{}
}

// session is the immutable lease/generator pair the loop publishes for Next to read
// lock-free. The loop swaps it wholesale on re-acquire; only the generator's own
// deadline clock changes in place on a published session.
type session struct {
	lease Lease
	gen   *snowflake.Generator
}

// ManagerOption customizes a Manager.
type ManagerOption func(*Manager)

// WithTTL sets the lease duration requested on acquire/renew.
func WithTTL(d time.Duration) ManagerOption { return func(m *Manager) { m.ttl = d } }

// WithMargin sets the safety margin subtracted from the TTL when computing the
// local minting deadline (deadline = send + (ttl − margin)).
func WithMargin(d time.Duration) ManagerOption { return func(m *Manager) { m.margin = d } }

// WithRenewInterval sets the heartbeat period. Keep it below ttl − margin.
func WithRenewInterval(d time.Duration) ManagerOption {
	return func(m *Manager) { m.renewInterval = d }
}

// WithClock injects the time source, which must be monotonic in production
// (time.Now is); tests pass a controllable fake shared with the generator.
func WithClock(now func() time.Time) ManagerOption { return func(m *Manager) { m.now = now } }

// NewManager builds a Manager that leases worker ids from store under owner. It
// rejects an invalid cadence: a renewInterval that does not leave room for a
// renewal before the deadline, or a margin that would push the deadline past the
// lease, are configuration errors rather than runtime surprises.
func NewManager(store LeaseStore, owner string, opts ...ManagerOption) (*Manager, error) {
	m := &Manager{
		store:          store,
		owner:          owner,
		ttl:            defaultTTL,
		margin:         defaultMargin,
		renewInterval:  defaultRenewInterval,
		now:            time.Now,
		beatTimeout:    defaultBeatTimeout,
		releaseTimeout: defaultReleaseTimeout,
	}
	for _, opt := range opts {
		opt(m)
	}
	if err := m.validateCadence(); err != nil {
		return nil, err
	}
	return m, nil
}

// validateCadence enforces the invariants the lease loop relies on. The order is
// deliberate so each check can assume the previous ones hold (e.g. ttl > margin
// before comparing against ttl − margin).
func (m *Manager) validateCadence() error {
	switch {
	case m.ttl <= 0:
		return fmt.Errorf("workerlease: ttl must be positive (got %s)", m.ttl)
	case m.margin < 0:
		return fmt.Errorf("workerlease: margin must not be negative (got %s)", m.margin)
	case m.ttl <= m.margin:
		return fmt.Errorf("workerlease: ttl (%s) must exceed margin (%s)", m.ttl, m.margin)
	case m.renewInterval <= 0:
		return fmt.Errorf("workerlease: renew interval must be positive (got %s)", m.renewInterval)
	case m.renewInterval >= m.ttl-m.margin:
		return fmt.Errorf("workerlease: renew interval (%s) must be shorter than ttl−margin (%s)", m.renewInterval, m.ttl-m.margin)
	}
	return nil
}

// Start acquires the first lease, publishes the generator with its initial deadline,
// and launches the renewal loop. A second call on a running Manager returns
// ErrAlreadyStarted (a non-nil cur is the running marker); a failed Start leaves cur
// nil and is retryable. The loop runs until Stop or ctx cancellation, releasing the
// lease on either exit.
func (m *Manager) Start(ctx context.Context) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	if m.cur.Load() != nil {
		return ErrAlreadyStarted
	}

	lease, gen, err := m.acquire(ctx)
	if err != nil {
		return err
	}
	m.cur.Store(&session{lease: lease, gen: gen})
	stop := make(chan struct{})
	done := make(chan struct{})
	m.stop, m.done = stop, done
	go m.run(ctx, stop, done)
	m.logLease("workerlease.acquired", lease)
	return nil
}

// run is the lease loop: it renews on each beat and, on either ctx cancellation or a
// Stop signal, tears the lease down. The ticker and lease ownership are stack-local
// here so the whole lifecycle reads top to bottom. stop and done are the caller's
// channels for this run, closed/observed only here. ctx flows straight into each
// beat, so a parent-ctx cancellation interrupts an in-flight renewal; Stop does not
// interrupt a beat, it just ends the loop once the current beat (bounded by the beat
// timeout) returns.
func (m *Manager) run(ctx context.Context, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer m.releaseAndClear()

	ticker := time.NewTicker(m.renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			m.beat(ctx)
		}
	}
}

// Next mints the next id from the current generator, or ErrNotStarted if no lease is
// live. The generator's own fail-closed errors (lease expired, clock regression)
// surface unchanged.
func (m *Manager) Next() (int64, error) {
	s := m.cur.Load()
	if s == nil {
		return 0, ErrNotStarted
	}
	return s.gen.Next()
}

// Stop signals the loop to exit and waits for it to release the lease. It is meant to
// be called once after Start; calling it before Start, or after the loop has already
// exited (e.g. ctx cancellation), is a no-op because cur is nil.
func (m *Manager) Stop() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	if m.cur.Load() == nil {
		return
	}
	close(m.stop)
	<-m.done
}

// acquire takes a lease and builds a fresh generator with its initial deadline. It
// does not mutate Manager state; callers install the returned pair only after they
// know the lifecycle still needs it.
func (m *Manager) acquire(ctx context.Context) (Lease, *snowflake.Generator, error) {
	sendAt := m.now()
	lease, err := m.store.Acquire(ctx, m.owner, m.ttl)
	if err != nil {
		return Lease{}, nil, err
	}
	gen, err := snowflake.NewGenerator(int64(lease.WorkerID), snowflake.WithClock(m.now))
	if err != nil {
		// Release the lease we just took so a rejected worker id is not leaked until
		// its TTL expires. The concrete repo's CHECK makes this unreachable, but the
		// LeaseStore interface permits an out-of-range id.
		_ = m.store.Release(ctx, lease)
		return Lease{}, nil, fmt.Errorf("workerlease: build generator: %w", err)
	}
	gen.Renew(sendAt.Add(m.ttl - m.margin))
	return lease, gen, nil
}

// beat performs one renewal: renew and, on success, extend the local deadline; on a
// transient error leave the deadline to lapse on its own and retry next beat; on a
// confirmed loss halt minting and re-acquire a fresh worker id. Its database work is
// bounded by beatTimeout so a hung store call cannot stall the loop.
func (m *Manager) beat(ctx context.Context) {
	sendAt := m.now()
	s := m.cur.Load()
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, m.beatTimeout)
	defer cancel()
	held, err := m.store.Renew(ctx, s.lease, m.ttl)
	switch {
	case err != nil:
		slog.Warn("workerlease.renew_error", "error", err)
	case !held:
		slog.Warn("workerlease.lease_lost")
		m.handleLoss(ctx, s)
	default:
		s.gen.Renew(sendAt.Add(m.ttl - m.margin))
	}
}

// handleLoss halts minting immediately, then re-acquires a fresh worker id. If the
// re-acquire fails (no capacity / transient), minting stays halted and the next beat
// retries. The loop is cur's only writer, so publishing the new session needs no
// compare-and-swap guard.
func (m *Manager) handleLoss(ctx context.Context, lost *session) {
	// Halt minting immediately: a zero deadline returns the generator to its
	// fail-closed state. We must stop now (not wait for the old deadline) because
	// another process may take our worker id once the lease is lost.
	lost.gen.Renew(time.Time{})
	lease, gen, err := m.acquire(ctx)
	if err != nil {
		slog.Warn("workerlease.reacquire_failed", "error", err)
		return
	}
	m.cur.Store(&session{lease: lease, gen: gen})
	m.logLease("workerlease.reacquired", lease)
}

// releaseAndClear releases the live lease and clears the running marker. It runs from
// the loop's deferred teardown on both exit paths (Stop and ctx cancellation). The
// lease is released before cur is cleared so a restart that observes a nil cur can
// never collide with the lease this loop still holds.
func (m *Manager) releaseAndClear() {
	s := m.cur.Load()
	if s == nil {
		return
	}
	s.gen.Renew(time.Time{})
	// A fresh background context, not the loop's (possibly already-cancelled) ctx, so
	// the release still runs on the ctx-cancellation exit path — bounded so a hung
	// release cannot stall teardown.
	ctx, cancel := context.WithTimeout(context.Background(), m.releaseTimeout)
	defer cancel()
	if err := m.store.Release(ctx, s.lease); err != nil {
		slog.Warn("workerlease.release_error", "error", err)
	}
	m.cur.Store(nil)
}

func (m *Manager) logLease(msg string, lease Lease) {
	slog.Info(msg, "worker_id", lease.WorkerID, "owner", m.owner)
}

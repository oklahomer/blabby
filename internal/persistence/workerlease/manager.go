package workerlease

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/oklahomer/blabby/internal/snowflake"
)

// Lease cadence defaults. renewInterval must stay below ttl−margin so a renewal is
// attempted (and, on failure, retried) before the local deadline lapses.
const (
	defaultTTL           = 30 * time.Second
	defaultMargin        = 5 * time.Second
	defaultRenewInterval = 10 * time.Second
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
type Manager struct {
	store         LeaseStore
	owner         string
	ttl           time.Duration
	margin        time.Duration
	renewInterval time.Duration
	now           func() time.Time

	lifecycleMu sync.Mutex

	mu    sync.Mutex
	state managerState
}

type managerState struct {
	gen    *snowflake.Generator
	lease  Lease
	cancel context.CancelFunc
	done   chan struct{}
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
		store:         store,
		owner:         owner,
		ttl:           defaultTTL,
		margin:        defaultMargin,
		renewInterval: defaultRenewInterval,
		now:           time.Now,
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

// Start acquires the first lease, installs the generator with its initial deadline,
// and launches the renewal heartbeat. A second call on a running Manager returns
// ErrAlreadyStarted (m.cancel is the live-heartbeat marker); a failed Start leaves
// it unset and is retryable. The heartbeat runs until Stop or ctx cancellation.
func (m *Manager) Start(ctx context.Context) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	if _, ok := m.snapshot(); ok {
		return ErrAlreadyStarted
	}

	lease, gen, err := m.acquire(ctx)
	if err != nil {
		return err
	}
	hbCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	m.install(managerState{
		gen:    gen,
		lease:  lease,
		cancel: cancel,
		done:   done,
	})
	go func() {
		defer close(done)
		m.heartbeat(hbCtx)
	}()
	m.logLease("workerlease.acquired", lease)
	return nil
}

// Next mints the next id from the current generator, or ErrNotStarted if Start has
// not run. The generator's own fail-closed errors (lease expired, clock regression)
// surface unchanged.
func (m *Manager) Next() (int64, error) {
	state, ok := m.snapshot()
	if !ok {
		return 0, ErrNotStarted
	}
	return state.gen.Next()
}

// Stop terminates the heartbeat and releases the current lease (best-effort). It is
// meant to be called once after Start; calling it before Start is a no-op.
func (m *Manager) Stop() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	state, ok := m.snapshot()
	if !ok {
		return
	}
	m.clear()
	state.gen.Renew(time.Time{})
	state.cancel()
	<-state.done
	if err := m.store.Release(context.Background(), state.lease); err != nil {
		slog.Warn("workerlease.release_error", "error", err)
	}
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

func (m *Manager) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(m.renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

// tick performs one renewal beat: renew and, on success, extend the local deadline;
// on a transient error leave the deadline to lapse on its own and retry next beat;
// on a confirmed loss halt minting and re-acquire a fresh worker id.
func (m *Manager) tick(ctx context.Context) {
	sendAt := m.now()
	state, ok := m.snapshot()
	if !ok {
		return
	}
	held, err := m.store.Renew(ctx, state.lease, m.ttl)
	switch {
	case err != nil:
		slog.Warn("workerlease.renew_error", "error", err)
	case !held:
		slog.Warn("workerlease.lease_lost")
		m.handleLoss(ctx, state)
	default:
		state.gen.Renew(sendAt.Add(m.ttl - m.margin))
	}
}

// handleLoss halts minting immediately, then re-acquires a fresh worker id. If the
// re-acquire fails (no capacity / transient), minting stays halted and the next
// beat retries.
func (m *Manager) handleLoss(ctx context.Context, lost managerState) {
	// Halt minting immediately: a zero deadline returns the generator to its
	// fail-closed state. We must stop now (not wait for the old deadline) because
	// another process may take our worker id once the lease is lost.
	lost.gen.Renew(time.Time{})
	lease, gen, err := m.acquire(ctx)
	if err != nil {
		slog.Warn("workerlease.reacquire_failed", "error", err)
		return
	}
	if !m.replace(lost, lease, gen) {
		// Stop won the race while we were acquiring; do not hold a lease the stopped
		// manager can no longer renew.
		if err := m.store.Release(context.Background(), lease); err != nil {
			slog.Warn("workerlease.release_error", "error", err)
		}
		return
	}
	m.logLease("workerlease.reacquired", lease)
}

func (m *Manager) snapshot() (managerState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.gen == nil {
		return managerState{}, false
	}
	return m.state, true
}

func (m *Manager) install(state managerState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = state
}

func (m *Manager) replace(old managerState, lease Lease, gen *snowflake.Generator) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	// If the installed state no longer matches the one we set out to replace, Stop
	// (or another transition) won the race while we were acquiring; the gen check
	// alone covers it, since clear zeroes the whole state.
	if m.state.gen != old.gen || m.state.lease != old.lease {
		return false
	}
	m.state.gen = gen
	m.state.lease = lease
	return true
}

func (m *Manager) clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = managerState{}
}

func (m *Manager) logLease(msg string, lease Lease) {
	slog.Info(msg, "worker_id", lease.WorkerID, "owner", m.owner)
}

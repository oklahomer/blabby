// Package snowflake mints 63-bit, time-ordered identifiers in one shared number
// space across users, rooms, and events.
//
// Layout (high to low bits):
//
//	41 bits  milliseconds since Epoch (2026-01-01T00:00:00Z, ~69 years)
//	10 bits  worker id (0..1023, from a worker_lease)
//	12 bits  per-worker sequence within the millisecond (0..4095)
//
// A Generator mints only while it holds an unexpired lease, gated on a local
// monotonic deadline set via Renew. Minting is fail-closed: a lapsed lease, a
// backwards clock, or sequence exhaustion that cannot advance returns an error
// rather than risk a duplicate or out-of-order id.
//
// The Generator never compares the database's lease expiry (a DB-clock value)
// against its own wall clock — NTP steps or host↔DB skew could let it mint past a
// lease that already expired in DB time. Instead the lease owner translates each
// successful renewal into a local monotonic deadline and passes it to Renew; Next
// mints only while the injected clock is before that deadline.
package snowflake

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	timestampBits = 41
	workerIDBits  = 10
	sequenceBits  = 12

	// MaxWorkerID is the largest worker id that fits the 10-bit field.
	MaxWorkerID = (1 << workerIDBits) - 1 // 1023

	maxSequence  = (1 << sequenceBits) - 1  // 4095
	maxTimestamp = (1 << timestampBits) - 1 // ~69 years of milliseconds

	timestampShift = workerIDBits + sequenceBits // 22
	workerIDShift  = sequenceBits                // 12
)

// defaultEpoch is the fixed, immutable origin for the timestamp component. It must
// never change: moving it would renumber every previously minted id. It is
// unexported so no importer can reassign it — read it through [Epoch].
var defaultEpoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// Epoch returns the timestamp origin every id is measured from. time.Time is a
// value type, so callers receive an immutable copy they cannot use to mutate the
// generator's epoch.
func Epoch() time.Time { return defaultEpoch }

// Sentinel errors returned by Next.
var (
	// ErrLeaseExpired means the local monotonic lease deadline has passed (or was
	// never set). Minting stays disabled until Renew extends it.
	ErrLeaseExpired = errors.New("snowflake: worker lease expired")
	// ErrClockRegression means the clock moved backwards relative to the last
	// minted id, which would risk reusing or reordering ids.
	ErrClockRegression = errors.New("snowflake: clock moved backwards")
	// ErrEpochExhausted means the timestamp no longer fits the 41-bit field (the
	// generator has outlived its ~69-year epoch window).
	ErrEpochExhausted = errors.New("snowflake: timestamp exceeds epoch range")
	// ErrClockBeforeEpoch means the clock sits at or before the epoch, which would
	// yield a zero or negative id — a non-positive value the id package rejects.
	ErrClockBeforeEpoch = errors.New("snowflake: clock at or before epoch")
)

// Generator is a goroutine-safe Snowflake id source bound to one worker id.
type Generator struct {
	mu       sync.Mutex
	workerID int64
	epoch    time.Time
	now      func() time.Time

	lastMillis    int64
	sequence      int64
	leaseDeadline time.Time
}

// Option customizes a Generator. The production defaults (the package Epoch and
// time.Now) are correct on their own; options exist so tests can inject a
// controllable clock and a distinct epoch.
type Option func(*Generator)

// WithClock injects the time source. It must return monotonic readings in
// production, which time.Now does; tests pass a controllable fake.
func WithClock(now func() time.Time) Option {
	return func(g *Generator) { g.now = now }
}

// WithEpoch overrides the timestamp origin. Production uses the package default
// epoch; this exists for tests.
func WithEpoch(epoch time.Time) Option {
	return func(g *Generator) { g.epoch = epoch }
}

// NewGenerator builds a Generator for workerID (0..MaxWorkerID). It starts with
// no lease, so Next returns ErrLeaseExpired until Renew is called with a future
// deadline — the worker_lease renewal loop drives that.
func NewGenerator(workerID int64, opts ...Option) (*Generator, error) {
	if workerID < 0 || workerID > MaxWorkerID {
		return nil, fmt.Errorf("snowflake: worker id %d out of range 0..%d", workerID, MaxWorkerID)
	}
	g := &Generator{
		workerID:   workerID,
		epoch:      defaultEpoch,
		now:        time.Now,
		lastMillis: -1, // so the first mint always takes the fresh-millisecond path
	}
	for _, opt := range opts {
		opt(g)
	}
	return g, nil
}

// Renew sets the local monotonic deadline until which minting is permitted. The
// worker_lease renewal loop calls it on each successful acquire/renew with a
// deadline anchored to the request-send instant plus (TTL − margin). Minting
// stops once the injected clock reaches the deadline.
func (g *Generator) Renew(deadline time.Time) {
	g.mu.Lock()
	g.leaseDeadline = deadline
	g.mu.Unlock()
}

// Next mints the next id. It is fail-closed: ErrLeaseExpired if the lease deadline
// has passed, ErrClockRegression if the clock moved backwards, ErrEpochExhausted
// if the timestamp overflows its field. The returned id is positive and strictly
// increasing for a given worker.
func (g *Generator) Next() (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()
	if !now.Before(g.leaseDeadline) {
		return 0, ErrLeaseExpired
	}
	millis := g.millisSinceEpoch(now)
	if millis < 0 {
		return 0, fmt.Errorf("%w: %d ms before epoch", ErrClockBeforeEpoch, -millis)
	}

	// Resolve the millisecond and sequence into locals; the generator's state is
	// committed only once the resulting id is known valid, so a failed mint mutates
	// nothing.
	var seq int64
	switch {
	case millis < g.lastMillis:
		return 0, fmt.Errorf("%w: now=%d last=%d", ErrClockRegression, millis, g.lastMillis)
	case millis == g.lastMillis:
		seq = g.sequence + 1
		if seq > maxSequence {
			// This millisecond is full. Spin to the next one, re-checking the
			// lease each iteration so a spin can never outlive the lease.
			next, err := g.spinNextMillis(g.lastMillis)
			if err != nil {
				return 0, err
			}
			millis, seq = next, 0
		}
	default: // millis > g.lastMillis
		// A fresh millisecond restarts the sequence at zero (seq's zero value).
	}

	if millis > maxTimestamp {
		return 0, fmt.Errorf("%w: %d > %d", ErrEpochExhausted, millis, maxTimestamp)
	}

	id := millis<<timestampShift | g.workerID<<workerIDShift | seq
	if id <= 0 {
		// Reachable only at the exact epoch millisecond with worker 0 on the first
		// mint; a non-positive id would violate the id package's invariant.
		return 0, ErrClockBeforeEpoch
	}

	g.lastMillis = millis
	g.sequence = seq
	return id, nil
}

// spinNextMillis busy-waits until the clock advances past after, returning the
// new millisecond. It re-checks the lease on every read so an exhausted-sequence
// spin stays fail-closed.
func (g *Generator) spinNextMillis(after int64) (int64, error) {
	for {
		now := g.now()
		if !now.Before(g.leaseDeadline) {
			return 0, ErrLeaseExpired
		}
		if m := g.millisSinceEpoch(now); m > after {
			return m, nil
		}
	}
}

func (g *Generator) millisSinceEpoch(t time.Time) int64 {
	return t.Sub(g.epoch).Milliseconds()
}

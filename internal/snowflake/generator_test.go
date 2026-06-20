package snowflake

import (
	"errors"
	"testing"
	"time"
)

// decode splits an id back into its three fields for assertions.
func decode(id int64) (millis, worker, seq int64) {
	return id >> timestampShift, (id >> workerIDShift) & MaxWorkerID, id & maxSequence
}

// fixedClock returns a clock that always reports t.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestNewGeneratorRejectsWorkerIDOutOfRange(t *testing.T) {
	for _, wid := range []int64{-1, MaxWorkerID + 1, 9999} {
		if _, err := NewGenerator(wid); err == nil {
			t.Errorf("NewGenerator(%d): expected error, got nil", wid)
		}
	}
	for _, wid := range []int64{0, 1, MaxWorkerID} {
		if _, err := NewGenerator(wid); err != nil {
			t.Errorf("NewGenerator(%d): unexpected error: %v", wid, err)
		}
	}
}

func TestNextFailsClosedWithoutLease(t *testing.T) {
	now := defaultEpoch.Add(time.Hour)
	g, err := NewGenerator(7, WithClock(fixedClock(now)))
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}
	// No Renew has been called, so the zero-value deadline is in the past.
	if _, err := g.Next(); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("Next without lease: got %v, want ErrLeaseExpired", err)
	}
}

func TestNextEncodesFieldsAndIncrementsSequence(t *testing.T) {
	const worker = 42
	now := defaultEpoch.Add(90 * time.Minute)
	g, err := NewGenerator(worker, WithClock(fixedClock(now)))
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}
	g.Renew(now.Add(time.Minute))

	wantMillis := now.Sub(defaultEpoch).Milliseconds()
	var prev int64
	for i := int64(0); i < 5; i++ {
		id, err := g.Next()
		if err != nil {
			t.Fatalf("Next #%d: %v", i, err)
		}
		if id <= 0 {
			t.Fatalf("Next #%d: id %d is not positive", i, id)
		}
		if id <= prev {
			t.Fatalf("Next #%d: id %d not strictly greater than previous %d", i, id, prev)
		}
		prev = id

		millis, gotWorker, seq := decode(id)
		if millis != wantMillis {
			t.Errorf("Next #%d: millis = %d, want %d", i, millis, wantMillis)
		}
		if gotWorker != worker {
			t.Errorf("Next #%d: worker = %d, want %d", i, gotWorker, worker)
		}
		if seq != i {
			t.Errorf("Next #%d: seq = %d, want %d (same millisecond increments)", i, seq, i)
		}
	}
}

func TestNextSpinsToNextMillisOnSequenceExhaustion(t *testing.T) {
	base := defaultEpoch.Add(2 * time.Hour)
	// All top-of-Next reads for the first maxSequence+2 mints land on the same
	// millisecond; the spin that the exhausting mint triggers then sees base+1ms.
	var calls int
	clk := func() time.Time {
		calls++
		if calls <= maxSequence+2 {
			return base
		}
		return base.Add(time.Millisecond)
	}
	g, err := NewGenerator(1, WithClock(clk))
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}
	g.Renew(base.Add(time.Hour))

	// Fill the millisecond: sequences 0..maxSequence (maxSequence+1 ids).
	var last int64
	for i := 0; i <= maxSequence; i++ {
		id, err := g.Next()
		if err != nil {
			t.Fatalf("Next #%d: %v", i, err)
		}
		last = id
	}
	if _, _, seq := decode(last); seq != maxSequence {
		t.Fatalf("last filled seq = %d, want %d", seq, maxSequence)
	}

	// The next mint exhausts the sequence and must spin to base+1ms, seq 0.
	id, err := g.Next()
	if err != nil {
		t.Fatalf("exhausting Next: %v", err)
	}
	if id <= last {
		t.Fatalf("exhausting id %d not greater than %d", id, last)
	}
	millis, _, seq := decode(id)
	if want := base.Add(time.Millisecond).Sub(defaultEpoch).Milliseconds(); millis != want {
		t.Errorf("spun millis = %d, want %d", millis, want)
	}
	if seq != 0 {
		t.Errorf("spun seq = %d, want 0", seq)
	}
}

func TestNextRejectsClockRegression(t *testing.T) {
	base := defaultEpoch.Add(3 * time.Hour)
	current := base
	g, err := NewGenerator(1, WithClock(func() time.Time { return current }))
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}
	g.Renew(base.Add(time.Hour))

	if _, err := g.Next(); err != nil {
		t.Fatalf("first Next: %v", err)
	}
	current = base.Add(-2 * time.Millisecond) // clock jumps backwards
	if _, err := g.Next(); !errors.Is(err, ErrClockRegression) {
		t.Fatalf("Next after regression: got %v, want ErrClockRegression", err)
	}
}

func TestNextStopsMintingOnceLeaseDeadlinePasses(t *testing.T) {
	base := defaultEpoch.Add(4 * time.Hour)
	current := base
	g, err := NewGenerator(1, WithClock(func() time.Time { return current }))
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}
	deadline := base.Add(time.Millisecond)
	g.Renew(deadline)

	if _, err := g.Next(); err != nil {
		t.Fatalf("Next within lease: %v", err)
	}
	current = deadline // clock has reached the deadline; minting must stop
	if _, err := g.Next(); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("Next at deadline: got %v, want ErrLeaseExpired", err)
	}
}

func TestNextRejectsAtOrBeforeEpoch(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
	}{
		{name: "exactly at epoch with worker 0 yields a zero id", now: defaultEpoch},
		{name: "one millisecond before epoch yields a negative id", now: defaultEpoch.Add(-time.Millisecond)},
		{name: "well before epoch", now: defaultEpoch.Add(-time.Hour)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, err := NewGenerator(0, WithClock(fixedClock(tc.now)))
			if err != nil {
				t.Fatalf("NewGenerator: %v", err)
			}
			g.Renew(tc.now.Add(time.Hour)) // lease is valid; the epoch bound must still bite
			if _, err := g.Next(); !errors.Is(err, ErrClockBeforeEpoch) {
				t.Fatalf("Next: got %v, want ErrClockBeforeEpoch", err)
			}
		})
	}
}

func TestNextAtEpochWithNonZeroWorkerIsPositive(t *testing.T) {
	const worker = 7
	g, err := NewGenerator(worker, WithClock(fixedClock(defaultEpoch)))
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}
	g.Renew(defaultEpoch.Add(time.Hour))

	// millis == 0 is a legitimate timestamp; only the all-zero corner is rejected,
	// so a non-zero worker at the epoch still mints a positive id.
	id, err := g.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if id <= 0 {
		t.Fatalf("id %d is not positive", id)
	}
	millis, gotWorker, seq := decode(id)
	if millis != 0 || gotWorker != worker || seq != 0 {
		t.Fatalf("decoded (millis=%d, worker=%d, seq=%d), want (0, %d, 0)", millis, gotWorker, seq, worker)
	}
}

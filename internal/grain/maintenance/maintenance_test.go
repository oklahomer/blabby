package maintenance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"

	maintenancepb "github.com/oklahomer/blabby/gen/maintenance"
	clustertest "github.com/oklahomer/blabby/internal/testutil/cluster"
)

// fakeSweeper is a configurable Sweeper. delay holds the sweep in-flight for a
// bounded time; gate holds it until the test closes the channel, making
// "the worker is still running" a structural fact rather than a timing bet.
// Both intentionally ignore context so tests can observe future timeouts even
// when the underlying work keeps running; the worker always finishes on its
// own (tests close the gate before cleanup), so it never blocks cluster
// shutdown.
type fakeSweeper struct {
	mu              sync.Mutex
	calls           int
	lastNow         time.Time
	lastHadDeadline bool
	deleted         int64
	err             error
	delay           time.Duration
	gate            <-chan struct{}
}

func (f *fakeSweeper) Sweep(ctx context.Context, now time.Time) (int64, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.gate != nil {
		<-f.gate
	}
	_, hasDeadline := ctx.Deadline()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastNow = now
	f.lastHadDeadline = hasDeadline
	return f.deleted, f.err
}

func (f *fakeSweeper) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeSweeper) lastNowValue() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastNow
}

func (f *fakeSweeper) hadDeadline() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastHadDeadline
}

type contextAwareSweeper struct{}

func (contextAwareSweeper) Sweep(ctx context.Context, _ time.Time) (int64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

type panicSweeper struct{}

func (panicSweeper) Sweep(context.Context, time.Time) (int64, error) {
	panic("sweep panic")
}

func TestNewKindPanicsOnNilSweeper(t *testing.T) {
	assertPanics(t, func() {
		NewKind(nil)
	})
}

func TestNewKindPanicsOnInvalidTimeouts(t *testing.T) {
	tests := []struct {
		name          string
		futureTimeout time.Duration
		dbTimeout     time.Duration
	}{
		{name: "zero future timeout", futureTimeout: 0, dbTimeout: time.Second},
		{name: "negative future timeout", futureTimeout: -time.Nanosecond, dbTimeout: time.Second},
		{name: "zero DB timeout", futureTimeout: time.Second, dbTimeout: 0},
		{name: "negative DB timeout", futureTimeout: time.Second, dbTimeout: -time.Nanosecond},
		{name: "DB timeout equals future timeout", futureTimeout: time.Second, dbTimeout: time.Second},
		{name: "DB timeout exceeds future timeout", futureTimeout: time.Second, dbTimeout: 2 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertPanics(t, func() {
				NewKind(&fakeSweeper{}, WithTimeouts(tt.futureTimeout, tt.dbTimeout))
			})
		})
	}
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()

	fn()
}

func TestSweepWorker_RunsSweepAndReplies(t *testing.T) {
	system := actor.NewActorSystem()
	t.Cleanup(func() { system.Shutdown() })

	fake := &fakeSweeper{deleted: 7}
	now := time.Unix(1000, 0).UTC()
	worker := system.Root.Spawn(actor.PropsFromProducer(func() actor.Actor {
		return newSweepWorker(fake, func() time.Time { return now }, time.Minute)
	}))

	res, err := system.Root.RequestFuture(worker, runSweep{}, 2*time.Second).Result()
	if err != nil {
		t.Fatalf("RequestFuture: %v", err)
	}
	got, ok := res.(sweepResult)
	if !ok {
		t.Fatalf("reply type %T, want sweepResult", res)
	}
	if got.deleted != 7 || got.err != nil {
		t.Fatalf("reply = %+v, want {deleted:7}", got)
	}
	if fake.callCount() != 1 {
		t.Fatalf("sweep called %d times, want 1", fake.callCount())
	}
	if !fake.lastNowValue().Equal(now) {
		t.Fatalf("sweep called with now=%v, want %v", fake.lastNowValue(), now)
	}
	if !fake.hadDeadline() {
		t.Fatal("sweep ran with an unbounded context; want a deadline (dbTimeout)")
	}
}

func TestSweepWorker_DBTimeoutReportsBeforeFutureTimeout(t *testing.T) {
	system := actor.NewActorSystem()
	t.Cleanup(func() { system.Shutdown() })

	worker := system.Root.Spawn(actor.PropsFromProducer(func() actor.Actor {
		return newSweepWorker(contextAwareSweeper{}, time.Now, 50*time.Millisecond)
	}))

	res, err := system.Root.RequestFuture(worker, runSweep{}, time.Second).Result()
	if err != nil {
		t.Fatalf("RequestFuture: %v", err)
	}
	got, ok := res.(sweepResult)
	if !ok {
		t.Fatalf("reply type %T, want sweepResult", res)
	}
	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("reply error = %v, want context deadline exceeded", got.err)
	}
}

func TestMaintenanceGrain_CoalescesConcurrentTriggers(t *testing.T) {
	fake := &fakeSweeper{deleted: 3, delay: 500 * time.Millisecond}
	c := clustertest.Start(t, NewKind(fake))
	client := maintenancepb.GetMaintenanceGrainGrainClient(c, PendingAccountGCIdentity)

	// The first trigger starts a sweep that runs for ~500ms, so it is still
	// in-flight when the second trigger arrives.
	first, err := client.SweepPendingAccounts(&maintenancepb.SweepPendingAccountsRequest{})
	if err != nil {
		t.Fatalf("trigger 1: %v", err)
	}
	if !first.Accepted {
		t.Fatal("first trigger should be accepted")
	}

	second, err := client.SweepPendingAccounts(&maintenancepb.SweepPendingAccountsRequest{})
	if err != nil {
		t.Fatalf("trigger 2: %v", err)
	}
	if second.Accepted {
		t.Fatal("a trigger during an in-flight sweep must be rejected (accepted=false)")
	}

	// Once the sweep completes, the worker replies, the continuation clears running,
	// and a later trigger is accepted again.
	if !eventuallyAccepted(t, client, time.Second) {
		t.Fatal("running flag did not clear after the worker replied")
	}
	if fake.callCount() < 1 {
		t.Fatalf("sweep ran %d times, want at least 1", fake.callCount())
	}
}

func TestMaintenanceGrain_FutureTimeoutClearsRunning(t *testing.T) {
	// The sweep blocks on a gate the test holds shut, so the worker is
	// structurally in-flight for the whole test — the future timeout (150ms)
	// is the only path that can clear `running`.
	gate := make(chan struct{})
	defer close(gate) // release the worker; it finishes on its own before cluster cleanup
	fake := &fakeSweeper{gate: gate}
	c := clustertest.Start(t, NewKind(fake, WithTimeouts(150*time.Millisecond, 100*time.Millisecond)))
	client := maintenancepb.GetMaintenanceGrainGrainClient(c, PendingAccountGCIdentity)

	first, err := client.SweepPendingAccounts(&maintenancepb.SweepPendingAccountsRequest{})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if !first.Accepted {
		t.Fatal("first trigger should be accepted")
	}

	// A retrigger is eventually accepted while the worker is still provably
	// blocked on the gate, which proves the future-timeout continuation — not
	// the worker's reply — cleared the flag.
	if !eventuallyAccepted(t, client, time.Second) {
		t.Fatal("running did not clear after the future timed out")
	}
}

func TestMaintenanceGrain_PanickingWorkerTimeoutClearsRunning(t *testing.T) {
	c := clustertest.Start(t, NewKind(panicSweeper{}, WithTimeouts(150*time.Millisecond, 100*time.Millisecond)))
	client := maintenancepb.GetMaintenanceGrainGrainClient(c, PendingAccountGCIdentity)

	first, err := client.SweepPendingAccounts(&maintenancepb.SweepPendingAccountsRequest{})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if !first.Accepted {
		t.Fatal("first trigger should be accepted")
	}

	second, err := client.SweepPendingAccounts(&maintenancepb.SweepPendingAccountsRequest{})
	if err != nil {
		t.Fatalf("trigger while worker is panicking: %v", err)
	}
	if second.Accepted {
		t.Fatal("trigger before the future timeout must be rejected")
	}

	if !eventuallyAccepted(t, client, time.Second) {
		t.Fatal("running flag did not clear after the panicking worker timed out")
	}
}

func eventuallyAccepted(t *testing.T, client *maintenancepb.MaintenanceGrainGrainClient, within time.Duration) bool {
	t.Helper()
	attempts := int(within / (20 * time.Millisecond))
	for i := 0; i < attempts; i++ {
		resp, err := client.SweepPendingAccounts(&maintenancepb.SweepPendingAccountsRequest{})
		if err != nil {
			t.Fatalf("poll trigger: %v", err)
		}
		if resp.Accepted {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

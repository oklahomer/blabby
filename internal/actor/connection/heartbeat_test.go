package connection

import (
	"sync"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
)

// timersProbe mirrors UserConnection's heartbeat wiring so the timers can be
// exercised without a WebSocket: one heartbeatTimers per producer call,
// started on Started, fed on tick/pong, stopped on Stopping.
type timersProbe struct {
	timers     *heartbeatTimers
	gotPing    chan struct{}
	gotTimeout chan struct{}
}

func newTimersProbe(cfg heartbeatConfig) *timersProbe {
	return &timersProbe{
		timers:     newHeartbeatTimers(cfg),
		gotPing:    make(chan struct{}, 1),
		gotTimeout: make(chan struct{}, 1),
	}
}

func (p *timersProbe) Receive(ctx actor.Context) {
	switch ctx.Message().(type) {
	case *actor.Started:
		p.timers.start(ctx)
	case *AppPingTick:
		select {
		case p.gotPing <- struct{}{}:
		default:
		}
		p.timers.ensureWatchdog(ctx)
	case *AppPongReceived:
		p.timers.resetWatchdog(ctx)
	case *PongTimeoutExpired:
		select {
		case p.gotTimeout <- struct{}{}:
		default:
		}
		p.timers.cancelWatchdog()
	case *actor.Stopping:
		p.timers.stop()
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected %s within 2s; never received", what)
	}
}

func TestHeartbeatTimers_SendsPingTickAndPongTimeout(t *testing.T) {
	system := actor.NewActorSystem()
	probe := newTimersProbe(heartbeatConfig{
		pingInterval: 20 * time.Millisecond,
		pongTimeout:  30 * time.Millisecond,
	})
	props := actor.PropsFromProducer(func() actor.Actor { return probe })

	pid := system.Root.Spawn(props)
	t.Cleanup(func() { _ = system.Root.PoisonFuture(pid).Wait() })

	waitSignal(t, probe.gotPing, "*AppPingTick")
	waitSignal(t, probe.gotTimeout, "*PongTimeoutExpired")
}

// TestHeartbeatTimers_PerActorStateAcrossPropsReuse is the regression test
// for per-Props heartbeat state. protoactor composes a Props' middleware
// chain once per Props, not per spawn, which is why the cancel handles must
// live on the actor: two actors spawned from ONE Props each own their timers,
// and stopping the first must not cancel the second's ping schedule.
func TestHeartbeatTimers_PerActorStateAcrossPropsReuse(t *testing.T) {
	system := actor.NewActorSystem()
	cfg := heartbeatConfig{
		pingInterval: 20 * time.Millisecond,
		// Long timeout: watchdog noise is irrelevant to this test.
		pongTimeout: 10 * time.Second,
	}

	var mu sync.Mutex
	var probes []*timersProbe
	props := actor.PropsFromProducer(func() actor.Actor {
		p := newTimersProbe(cfg)
		mu.Lock()
		probes = append(probes, p)
		mu.Unlock()
		return p
	})

	waitProbe := func(idx int) *timersProbe {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			if len(probes) > idx {
				p := probes[idx]
				mu.Unlock()
				return p
			}
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("probe %d was never produced", idx)
		return nil
	}

	pidA := system.Root.Spawn(props)
	probeA := waitProbe(0)
	waitSignal(t, probeA.gotPing, "actor A's first *AppPingTick")

	pidB := system.Root.Spawn(props)
	t.Cleanup(func() { _ = system.Root.PoisonFuture(pidB).Wait() })
	probeB := waitProbe(1)
	waitSignal(t, probeB.gotPing, "actor B's first *AppPingTick")

	// Stop A, then prove B's ping schedule is still alive: drain any
	// buffered tick and require a fresh one.
	if err := system.Root.PoisonFuture(pidA).Wait(); err != nil {
		t.Fatalf("stopping actor A: %v", err)
	}
	select {
	case <-probeB.gotPing:
	default:
	}
	waitSignal(t, probeB.gotPing, "actor B's *AppPingTick after actor A stopped")
}

func TestHeartbeatConfig_RequiresBothDurations(t *testing.T) {
	tests := []struct {
		name string
		cfg  heartbeatConfig
		want bool
	}{
		{name: "both set", cfg: heartbeatConfig{pingInterval: time.Second, pongTimeout: time.Second}, want: true},
		{name: "missing ping", cfg: heartbeatConfig{pongTimeout: time.Second}, want: false},
		{name: "missing timeout", cfg: heartbeatConfig{pingInterval: time.Second}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.enabled(); got != tc.want {
				t.Errorf("enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

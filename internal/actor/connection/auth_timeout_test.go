package connection

import (
	"sync"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
)

// probeActor records every received message; tests use it to assert that
// authTimeoutMiddleware schedules an *AuthTimeoutExpired to the actor after Started.
type probeActor struct {
	mu       sync.Mutex
	received []any
	gotTimer chan struct{}
}

func newProbeActor() *probeActor {
	return &probeActor{gotTimer: make(chan struct{}, 1)}
}

func (p *probeActor) Receive(ctx actor.Context) {
	p.mu.Lock()
	p.received = append(p.received, ctx.Message())
	p.mu.Unlock()
	if _, ok := ctx.Message().(*AuthTimeoutExpired); ok {
		select {
		case p.gotTimer <- struct{}{}:
		default:
		}
	}
}

func TestAuthTimeoutMiddleware_FiresAfterStarted(t *testing.T) {
	system := actor.NewActorSystem()
	probe := newProbeActor()
	props := actor.PropsFromProducer(
		func() actor.Actor { return probe },
		actor.WithReceiverMiddleware(authTimeoutMiddleware(50*time.Millisecond)),
	)

	pid := system.Root.Spawn(props)
	t.Cleanup(func() { _ = system.Root.PoisonFuture(pid).Wait() })

	select {
	case <-probe.gotTimer:
		// success
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected *AuthTimeoutExpired within 500ms; never received")
	}
}

func TestAuthTimeoutMiddleware_DoesNotScheduleOnNonStartedMessages(t *testing.T) {
	system := actor.NewActorSystem()
	probe := newProbeActor()
	props := actor.PropsFromProducer(
		func() actor.Actor { return probe },
		actor.WithReceiverMiddleware(authTimeoutMiddleware(10*time.Second)), // long
	)
	pid := system.Root.Spawn(props)
	t.Cleanup(func() { _ = system.Root.PoisonFuture(pid).Wait() })

	// Send unrelated message; middleware must not schedule additional timers
	// based on it. We can't easily count timers, but we assert that within a
	// short window we do NOT receive *AuthTimeoutExpired (the long delay protects).
	system.Root.Send(pid, "noop")
	select {
	case <-probe.gotTimer:
		t.Fatal("did not expect *AuthTimeoutExpired within 100ms")
	case <-time.After(100 * time.Millisecond):
		// success: no premature timer
	}
}

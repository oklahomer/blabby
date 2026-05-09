package connection

import (
	"sync"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
)

type heartbeatProbe struct {
	mu         sync.Mutex
	received   []any
	gotPing    chan struct{}
	gotTimeout chan struct{}
}

func newHeartbeatProbe() *heartbeatProbe {
	return &heartbeatProbe{
		gotPing:    make(chan struct{}, 1),
		gotTimeout: make(chan struct{}, 1),
	}
}

func (p *heartbeatProbe) Receive(ctx actor.Context) {
	p.mu.Lock()
	p.received = append(p.received, ctx.Message())
	p.mu.Unlock()

	switch ctx.Message().(type) {
	case *AppPingTick:
		select {
		case p.gotPing <- struct{}{}:
		default:
		}
	case *PongTimeoutExpired:
		select {
		case p.gotTimeout <- struct{}{}:
		default:
		}
	}
}

func TestAppHeartbeatMiddleware_SendsPingTickAndPongTimeout(t *testing.T) {
	system := actor.NewActorSystem()
	probe := newHeartbeatProbe()
	props := actor.PropsFromProducer(
		func() actor.Actor { return probe },
		actor.WithReceiverMiddleware(appHeartbeatMiddleware(heartbeatConfig{
			pingInterval: 20 * time.Millisecond,
			pongTimeout:  30 * time.Millisecond,
		})),
	)

	pid := system.Root.Spawn(props)
	t.Cleanup(func() { _ = system.Root.PoisonFuture(pid).Wait() })

	select {
	case <-probe.gotPing:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected *AppPingTick within 500ms; never received")
	}

	select {
	case <-probe.gotTimeout:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected *PongTimeoutExpired within 500ms; never received")
	}
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

package connection

import (
	"fmt"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/scheduler"
)

// HeartbeatCadence is the validated timing for a connection's application
// heartbeat: how often a ping is emitted and how long the peer has to answer
// before the watchdog declares it dead. The zero value disables the
// heartbeat. Non-zero values exist only through [NewHeartbeatCadence] (or
// [MustHeartbeatCadence]), which enforces pongTimeout > pingInterval: a pong
// re-arms the watchdog rather than canceling it (see [heartbeatTimers]), so
// a timeout at or below the interval would disconnect a healthy peer between
// pings.
type HeartbeatCadence struct {
	pingInterval time.Duration
	pongTimeout  time.Duration
}

// NewHeartbeatCadence validates and builds a HeartbeatCadence. It rejects a
// non-positive pingInterval and any pongTimeout not strictly greater than
// pingInterval.
func NewHeartbeatCadence(pingInterval, pongTimeout time.Duration) (HeartbeatCadence, error) {
	if pingInterval <= 0 {
		return HeartbeatCadence{}, fmt.Errorf("heartbeat: ping interval must be positive, got %v", pingInterval)
	}
	if pongTimeout <= pingInterval {
		return HeartbeatCadence{}, fmt.Errorf("heartbeat: pong timeout %v must exceed ping interval %v: a pong re-arms the watchdog, so a shorter timeout disconnects healthy peers between pings", pongTimeout, pingInterval)
	}
	return HeartbeatCadence{pingInterval: pingInterval, pongTimeout: pongTimeout}, nil
}

// MustHeartbeatCadence is NewHeartbeatCadence for constant call sites: it
// panics on an invalid pair, so a bad constant fails at package
// initialization instead of shipping a heartbeat that kills healthy peers.
func MustHeartbeatCadence(pingInterval, pongTimeout time.Duration) HeartbeatCadence {
	c, err := NewHeartbeatCadence(pingInterval, pongTimeout)
	if err != nil {
		panic(err)
	}
	return c
}

// enabled reports whether this cadence activates the heartbeat. Construction
// permits exactly two states: the zero value (disabled) and a validated pair.
func (c HeartbeatCadence) enabled() bool {
	return c != HeartbeatCadence{}
}

// heartbeatTimers owns the application-heartbeat schedule for one
// UserConnection: the repeating [AppPingTick] and the one-shot
// [PongTimeoutExpired] watchdog. The actor creates one instance per spawn (in
// the Props producer) and drives it from Receive, making the cancel handles
// per-actor state. Keeping them out of middleware closures is deliberate:
// protoactor composes a Props' receiver-middleware chain once per Props, not
// per spawn, so closure state there would be shared — and clobbered — by
// every actor the Props produces.
//
// A disabled cadence (the zero [HeartbeatCadence]) makes every method a
// no-op, so the actor calls them unconditionally. The guard is load-bearing
// on resetWatchdog: a client-sent "pong" frame decodes to [AppPongReceived]
// even when heartbeat is off, and must not arm a zero-duration watchdog.
type heartbeatTimers struct {
	cadence        HeartbeatCadence
	pingCancel     scheduler.CancelFunc
	watchdogCancel scheduler.CancelFunc
}

func newHeartbeatTimers(cadence HeartbeatCadence) *heartbeatTimers {
	return &heartbeatTimers{cadence: cadence}
}

// start arms the repeating ping tick. Called once, on *actor.Started.
func (t *heartbeatTimers) start(ctx actor.Context) {
	if !t.cadence.enabled() {
		return
	}
	sched := scheduler.NewTimerScheduler(ctx.ActorSystem().Root)
	t.pingCancel = sched.SendRepeatedly(t.cadence.pingInterval, t.cadence.pingInterval, ctx.Self(), &AppPingTick{})
}

// ensureWatchdog arms the pong watchdog unless one is already pending.
// Called on every ping tick, so a single watchdog spans consecutive
// unanswered pings instead of being pushed out by each new ping.
func (t *heartbeatTimers) ensureWatchdog(ctx actor.Context) {
	if t.watchdogCancel != nil {
		return
	}
	t.resetWatchdog(ctx)
}

// resetWatchdog re-arms the pong watchdog from now. Called when a pong
// arrives, granting the peer a fresh pongTimeout.
func (t *heartbeatTimers) resetWatchdog(ctx actor.Context) {
	if !t.cadence.enabled() {
		return
	}
	t.cancelWatchdog()
	sched := scheduler.NewTimerScheduler(ctx.ActorSystem().Root)
	t.watchdogCancel = sched.SendOnce(t.cadence.pongTimeout, ctx.Self(), &PongTimeoutExpired{})
}

// cancelWatchdog drops the pending watchdog, if any. Called when the
// watchdog fires (the handle is spent) and from stop.
func (t *heartbeatTimers) cancelWatchdog() {
	if t.watchdogCancel != nil {
		t.watchdogCancel()
		t.watchdogCancel = nil
	}
}

// stop cancels both timers. Called on *actor.Stopping; safe to call more
// than once.
func (t *heartbeatTimers) stop() {
	t.cancelWatchdog()
	if t.pingCancel != nil {
		t.pingCancel()
		t.pingCancel = nil
	}
}

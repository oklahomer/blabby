package connection

import (
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/scheduler"
)

type heartbeatConfig struct {
	pingInterval time.Duration
	pongTimeout  time.Duration
}

func (c heartbeatConfig) enabled() bool {
	return c.pingInterval > 0 && c.pongTimeout > 0
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
// A disabled config (see [heartbeatConfig.enabled]) makes every method a
// no-op, so the actor calls them unconditionally. The guard is load-bearing
// on resetWatchdog: a client-sent "pong" frame decodes to [AppPongReceived]
// even when heartbeat is off, and must not arm a zero-duration watchdog.
type heartbeatTimers struct {
	cfg            heartbeatConfig
	pingCancel     scheduler.CancelFunc
	watchdogCancel scheduler.CancelFunc
}

func newHeartbeatTimers(cfg heartbeatConfig) *heartbeatTimers {
	return &heartbeatTimers{cfg: cfg}
}

// start arms the repeating ping tick. Called once, on *actor.Started.
func (t *heartbeatTimers) start(ctx actor.Context) {
	if !t.cfg.enabled() {
		return
	}
	sched := scheduler.NewTimerScheduler(ctx.ActorSystem().Root)
	t.pingCancel = sched.SendRepeatedly(t.cfg.pingInterval, t.cfg.pingInterval, ctx.Self(), &AppPingTick{})
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
	if !t.cfg.enabled() {
		return
	}
	t.cancelWatchdog()
	sched := scheduler.NewTimerScheduler(ctx.ActorSystem().Root)
	t.watchdogCancel = sched.SendOnce(t.cfg.pongTimeout, ctx.Self(), &PongTimeoutExpired{})
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

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

func appHeartbeatMiddleware(cfg heartbeatConfig) actor.ReceiverMiddleware {
	return func(next actor.ReceiverFunc) actor.ReceiverFunc {
		var pingCancel scheduler.CancelFunc
		var watchdogCancel scheduler.CancelFunc

		cancelWatchdog := func() {
			if watchdogCancel != nil {
				watchdogCancel()
				watchdogCancel = nil
			}
		}
		cancelPing := func() {
			if pingCancel != nil {
				pingCancel()
				pingCancel = nil
			}
		}
		startWatchdog := func(ctx actor.ReceiverContext) {
			cancelWatchdog()
			sched := scheduler.NewTimerScheduler(ctx.ActorSystem().Root)
			watchdogCancel = sched.SendOnce(cfg.pongTimeout, ctx.Self(), &PongTimeoutExpired{})
		}
		ensureWatchdog := func(ctx actor.ReceiverContext) {
			if watchdogCancel != nil {
				return
			}
			startWatchdog(ctx)
		}

		return func(ctx actor.ReceiverContext, env *actor.MessageEnvelope) {
			switch env.Message.(type) {
			case *actor.Started:
				next(ctx, env)
				sched := scheduler.NewTimerScheduler(ctx.ActorSystem().Root)
				pingCancel = sched.SendRepeatedly(cfg.pingInterval, cfg.pingInterval, ctx.Self(), &AppPingTick{})
			case *AppPingTick:
				next(ctx, env)
				ensureWatchdog(ctx)
			case *AppPongReceived:
				next(ctx, env)
				startWatchdog(ctx)
			case *PongTimeoutExpired:
				cancelWatchdog()
				next(ctx, env)
			case *actor.Stopping, *actor.Stopped:
				cancelWatchdog()
				cancelPing()
				next(ctx, env)
			default:
				next(ctx, env)
			}
		}
	}
}

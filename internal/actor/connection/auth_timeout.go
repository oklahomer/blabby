package connection

import (
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/scheduler"
)

// authTimeoutMiddleware returns a ReceiverMiddleware that, on the actor's
// *actor.Started message, schedules a one-shot *AuthTimeoutExpired to the actor
// itself after d. After Started, the middleware is a passthrough.
//
// Why a middleware (and not a check inside the actor's Receive)? Two
// reasons. (1) It documents the ReceiverMiddleware shape so future
// middlewares (logging, rate-limit windows) can follow the same pattern.
// (2) It cleanly separates "schedule the deadline" from "decide what to
// do when it fires" — Receive only needs to handle *AuthTimeoutExpired; it does
// not need to know how the deadline was scheduled.
//
// The timer fires unconditionally; the receiver decides whether it still
// matters. Cancelling on auth-success is unnecessary complexity;
// postAuthBehavior ignores the timer message if it arrives after auth.
func authTimeoutMiddleware(d time.Duration) actor.ReceiverMiddleware {
	return func(next actor.ReceiverFunc) actor.ReceiverFunc {
		return func(ctx actor.ReceiverContext, env *actor.MessageEnvelope) {
			if _, ok := env.Message.(*actor.Started); ok {
				// Run next first so the actor's Started handler observes
				// initialization in source order. Then schedule the timer.
				next(ctx, env)
				sched := scheduler.NewTimerScheduler(ctx.ActorSystem().Root)
				sched.SendOnce(d, ctx.Self(), &AuthTimeoutExpired{})
				return
			}
			next(ctx, env)
		}
	}
}

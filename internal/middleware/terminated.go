package middleware

import (
	"github.com/asynkron/protoactor-go/actor"
)

// WatchedTerminated is the user-visible form of protoactor's
// *actor.Terminated for generated grain actors. Terminated implements
// actor.SystemMessage, and the protoc-gen-go-grain dispatch discards every
// SystemMessage before reaching its default (ReceiveDefault) arm — so a grain
// that relies on death-watch eviction would never see the raw notification.
// TranslateTerminated re-wraps it into this plain struct, which falls through
// the generated switch to ReceiveDefault.
type WatchedTerminated struct {
	// Who is the watched PID that stopped.
	Who *actor.PID
	// Why reports how the watch ended (stopped, not found, …).
	Why actor.TerminatedReason
}

// TranslateTerminated is a receiver middleware that re-wraps
// *actor.Terminated into *WatchedTerminated so a generated grain actor can
// handle death-watch notifications in ReceiveDefault. Install it on any grain
// kind that calls ctx.Watch, ahead of GrainLogging in the middleware list so
// the log stream records the message the grain actually receives (see
// MIDDLEWARE ORDER in the package doc).
func TranslateTerminated() actor.ReceiverMiddleware {
	return func(next actor.ReceiverFunc) actor.ReceiverFunc {
		return func(ctx actor.ReceiverContext, env *actor.MessageEnvelope) {
			if t, ok := env.Message.(*actor.Terminated); ok {
				next(ctx, &actor.MessageEnvelope{
					Header:  env.Header,
					Message: &WatchedTerminated{Who: t.Who, Why: t.Why},
					Sender:  env.Sender,
				})
				return
			}
			next(ctx, env)
		}
	}
}

package connection

import (
	"fmt"
	"log/slog"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/scheduler"

	userpb "github.com/oklahomer/blabby/gen/user"
)

// This file holds the connection side of the bidirectional watch (ADR-006):
// re-registering with the User grain when its watched activation dies.
// The behavior transitions these helpers feed stay in postAuthBehavior —
// helpers report domain outcomes, the dispatch site picks the next behavior.

// samePID reports whether a and b name the same actor by value. Terminated
// carries a deserialized PID, never the stored pointer, and actor.PID.Equal
// also compares RequestId — a future-correlation field this comparison must
// ignore — so match on Address and Id explicitly.
func samePID(a, b *actor.PID) bool {
	return a != nil && b != nil && a.Address == b.Address && a.Id == b.Id
}

// grainPIDFromResponse extracts the responding activation's PID from a
// successful RegisterConnection response. It returns nil when the grain did
// not report one, or reported a half-populated PID (both fields are required
// together per the proto contract) — version skew; callers log and proceed
// without the reverse watch rather than failing a registration that
// succeeded.
func grainPIDFromResponse(resp *userpb.RegisterConnectionResponse) *actor.PID {
	pid := resp.GetGrainPid()
	if pid.GetAddress() == "" || pid.GetId() == "" {
		return nil
	}
	return &actor.PID{Address: pid.GetAddress(), Id: pid.GetId()}
}

// reregister re-issues RegisterConnection for the authenticated user so a
// fresh User grain activation learns about this connection immediately
// instead of at the client's next reconnect (ADR-006). Addressing the grain
// by identity reactivates it, so the first attempt usually succeeds. It
// performs no protocol I/O and does not change actor state or behavior: it
// returns the new activation's PID on success — nil when the grain did not
// report one (version skew) — or an error when the call failed on transport
// or the grain reported an inline error.
//
// A node loss makes every affected connection re-register at once. ADR-006
// accepts that at current scale; add jitter only if measurement demands it.
func (uc *UserConnection) reregister(ctx actor.Context) (*actor.PID, error) {
	resp, err := uc.userClient.RegisterConnection(uc.userID.String(), &userpb.RegisterConnectionRequest{
		RequesterPid: &userpb.PID{Address: ctx.Self().Address, Id: ctx.Self().Id},
	})
	if err != nil {
		return nil, fmt.Errorf("re-register connection: %w", err)
	}
	if respErr := resp.GetError(); respErr != nil {
		return nil, fmt.Errorf("re-register connection: grain rejected: code=%d status=%q",
			respErr.GetCode(), respErr.GetStatus())
	}
	return grainPIDFromResponse(resp), nil
}

// recordReregisterFailure notes one failed re-register attempt: it logs the
// failure and, while the attempt budget lasts, schedules the next
// ReregisterRetry tick. It reports whether the budget is exhausted so the
// dispatch site decides the transition — closing the connection, which makes
// the client's reconnect the fallback (ADR-006).
func (uc *UserConnection) recordReregisterFailure(ctx actor.Context, err error) (exhausted bool) {
	uc.reregisterAttempts++
	slog.Warn(eventConnectionReregisterFailed, uc.logAttrs(ctx,
		"attempt", uc.reregisterAttempts,
		"max_attempts", reregisterMaxAttempts,
		"error", err,
	)...)
	if uc.reregisterAttempts >= reregisterMaxAttempts {
		return true
	}
	sched := scheduler.NewTimerScheduler(ctx.ActorSystem().Root)
	uc.reregisterRetryCancel = sched.SendOnce(uc.reregisterRetryDelay, ctx.Self(), &ReregisterRetry{})
	return false
}

// attemptReregister runs one re-register attempt and its bookkeeping: on
// success it adopts the fresh activation and watches its PID; on failure it
// records the attempt (scheduling a retry while the budget lasts) and
// reports whether the budget is exhausted. The dispatch site owns the
// resulting transition, so every Become stays visible in postAuthBehavior.
func (uc *UserConnection) attemptReregister(ctx actor.Context) (shouldClose bool) {
	grainPID, err := uc.reregister(ctx)
	if err != nil {
		return uc.recordReregisterFailure(ctx, err)
	}
	uc.recordReregisterSuccess(ctx, grainPID)
	if grainPID != nil {
		ctx.Watch(grainPID)
	}
	return false
}

// recordReregisterSuccess adopts the fresh activation PID and resets the
// attempt budget. A nil grainPID (version skew) is a degraded success: the
// registration itself landed, deliveries flow again, but with no PID to
// watch, self-healing degrades to client-driven recovery — strictly better
// than burning the retry budget on a registration that keeps succeeding.
// attemptReregister arms a fresh watch for every new activation PID, since
// a stale watch would reopen the silent gap (ADR-006).
func (uc *UserConnection) recordReregisterSuccess(ctx actor.Context, grainPID *actor.PID) {
	uc.grainPID = grainPID
	uc.reregisterAttempts = 0
	if grainPID == nil {
		slog.Warn(eventConnectionRegisterNoGrainPid, uc.logAttrs(ctx, "path", "reregister")...)
		return
	}
	slog.Info(eventConnectionReregisterSucceeded, uc.logAttrs(ctx,
		"grain_pid", grainPID.String(),
	)...)
}

// cancelReregisterRetry drops the pending retry timer, if any. Called on
// *actor.Stopping so a stray tick does not dead-letter after the actor stops.
func (uc *UserConnection) cancelReregisterRetry() {
	if uc.reregisterRetryCancel != nil {
		uc.reregisterRetryCancel()
		uc.reregisterRetryCancel = nil
	}
}

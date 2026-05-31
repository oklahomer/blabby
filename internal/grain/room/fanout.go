package room

import (
	"log/slog"

	"github.com/asynkron/protoactor-go/actor"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/middleware"
)

// fanoutNotify and fanoutForward are the jobs a Room grain hands to its
// dedicated fan-out child actor. Each carries a recipient snapshot, the
// already-built (and thereafter immutable) payload, and the log-context
// strings captured from the grain context — so the child never reads grain
// state or the grain's GrainContext on its own goroutine.
type fanoutNotify struct {
	recipients []id.UserID
	payload    *userpb.NotifyRoomEventRequest
	msgType    string
	grainKind  string
	grainID    string
}

type fanoutForward struct {
	recipients []id.UserID
	payload    *userpb.ForwardMessageRequest
	msgType    string
	grainKind  string
	grainID    string
}

// fanoutDispatcher decouples "decide to fan out" (in the command handler)
// from "where the fan-out runs". The methods take the live actor.Context
// because the production dispatcher sends to the child PID, and ctx.Send must
// use the current message's context.
type fanoutDispatcher interface {
	notify(ctx actor.Context, job *fanoutNotify)
	forward(ctx actor.Context, job *fanoutForward)
}

// actorDispatcher is the production dispatcher: it enqueues the job on the
// long-lived fan-out child's mailbox. The child processes its mailbox in
// order, so notifications are delivered in the order the grain issued them.
type actorDispatcher struct {
	pid *actor.PID
}

func (d *actorDispatcher) notify(ctx actor.Context, job *fanoutNotify) {
	ctx.Send(d.pid, job)
}

func (d *actorDispatcher) forward(ctx actor.Context, job *fanoutForward) {
	ctx.Send(d.pid, job)
}

// syncDispatcher runs fan-out inline on the caller's goroutine. It is the
// unit-test path (installed via the export_test seam); production always uses
// actorDispatcher. Running inline keeps fan-out assertions deterministic.
type syncDispatcher struct {
	notifier userNotifier
}

func (d *syncDispatcher) notify(_ actor.Context, job *fanoutNotify) {
	runNotify(d.notifier, job)
}

func (d *syncDispatcher) forward(_ actor.Context, job *fanoutForward) {
	runForward(d.notifier, job)
}

// fanoutWorker is the Room grain's dedicated fan-out child actor. Performing
// fan-out here keeps it off the grain's own message goroutine, so a command
// handler returns as soon as state is committed instead of blocking on the
// per-member notification RPCs. For the acting user's own echo those RPCs
// target the still-blocked caller, which would otherwise deadlock; see
// ADR-015.
type fanoutWorker struct {
	notifier userNotifier
}

func (w *fanoutWorker) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *fanoutNotify:
		runNotify(w.notifier, msg)
	case *fanoutForward:
		runForward(w.notifier, msg)
	}
}

// runNotify performs the best-effort NotifyRoomEvent fan-out. A failed
// recipient is logged and skipped (Phase 1 fan-out has no retries) and never
// aborts delivery to the remaining recipients.
func runNotify(notifier userNotifier, job *fanoutNotify) {
	for _, recipientID := range job.recipients {
		if err := notifier.NotifyRoomEvent(recipientID, job.payload); err != nil {
			slog.Warn(middleware.EventGrainFanoutError,
				"grain_type", job.grainKind,
				"grain_id", job.grainID,
				"msg_type", job.msgType,
				"recipient_id", recipientID,
				"error", err,
			)
		}
	}
}

// runForward performs the best-effort ForwardMessage fan-out with the same
// semantics as runNotify.
func runForward(notifier userNotifier, job *fanoutForward) {
	for _, recipientID := range job.recipients {
		if err := notifier.ForwardMessage(recipientID, job.payload); err != nil {
			slog.Warn(middleware.EventGrainFanoutError,
				"grain_type", job.grainKind,
				"grain_id", job.grainID,
				"msg_type", job.msgType,
				"recipient_id", recipientID,
				"error", err,
			)
		}
	}
}

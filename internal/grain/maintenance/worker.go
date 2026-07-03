package maintenance

import (
	"context"
	"time"

	"github.com/asynkron/protoactor-go/actor"
)

// runSweep tells a freshly spawned worker to run one sweep. The grain sends it as a
// request (RequestFuture), so the worker replies to the future via ctx.Respond.
type runSweep struct{}

// sweepResult is the worker's reply: the number of accounts deleted, or the error
// that stopped the sweep. It travels back to the grain over the request future.
type sweepResult struct {
	deleted int64
	err     error
}

// sweepWorker runs a single pending-account sweep and replies with the outcome, then
// stops. One worker is spawned per trigger, so it holds no state between sweeps. The
// sweep runs under a bounded context (dbTimeout) so a slow database call is
// cancelled rather than outliving the grain's wait for the reply.
type sweepWorker struct {
	sweeper   Sweeper
	now       func() time.Time
	dbTimeout time.Duration
}

func newSweepWorker(sweeper Sweeper, now func() time.Time, dbTimeout time.Duration) *sweepWorker {
	return &sweepWorker{sweeper: sweeper, now: now, dbTimeout: dbTimeout}
}

// Receive runs the sweep on runSweep, replies to the requesting future, and stops.
// Other messages are ignored: the worker exists only to perform its one job off the
// grain's mailbox.
func (w *sweepWorker) Receive(ctx actor.Context) {
	if _, ok := ctx.Message().(runSweep); !ok {
		return
	}
	dbCtx, cancel := context.WithTimeout(context.Background(), w.dbTimeout)
	defer cancel()
	deleted, err := w.sweeper.Sweep(dbCtx, w.now())
	ctx.Respond(sweepResult{deleted: deleted, err: err})
	ctx.Stop(ctx.Self())
}

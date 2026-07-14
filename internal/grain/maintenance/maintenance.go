// Package maintenance hosts blabby's periodic system jobs as a single cluster-wide
// grain. The grain coalesces trigger requests and runs each job in a child worker;
// the job's actual work (e.g. the pending-account sweep) lives in its own package
// and is injected as a small interface.
package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"

	maintenancepb "github.com/oklahomer/blabby/gen/maintenance"
	"github.com/oklahomer/blabby/internal/middleware"
)

// PendingAccountGCIdentity is the fixed cluster identity of the pending-account GC
// job. Every trigger addresses this one identity, so Proto.Cluster routes them all
// to a single activation — the cluster-wide coordination point for the job. A
// future maintenance job gets its own identity (one activation per job), so the
// per-activation coalescing state never spans jobs.
const PendingAccountGCIdentity = "pending-account-gc"

// kindName labels the grain in logs and the grain-logging middleware.
const kindName = "MaintenanceGrain"

// passivationTimeout of 0 disables passivation. The maintenance grain is one fixed
// system identity (not one activation per user or room), and it must not deactivate
// while a worker may still be running.
const passivationTimeout = 0

const (
	// sweepFutureTimeout bounds how long the grain waits for a worker to reply. On
	// timeout the reentrant continuation still runs (with an error) and clears the
	// running flag, so a hung or crashed worker can never wedge the job.
	sweepFutureTimeout = 45 * time.Second
	// sweepDBTimeout bounds the worker's database call. It is shorter than
	// sweepFutureTimeout so normal slow database work can return an explicit result
	// before the actor-level future timeout releases the grain's running flag.
	sweepDBTimeout = 30 * time.Second
)

// Sweeper runs one pending-account sweep, returning the number of accounts deleted.
// *accountgc.Sweeper satisfies it; the grain depends on the interface so it can be
// exercised with a fake.
type Sweeper interface {
	Sweep(ctx context.Context, now time.Time) (int64, error)
}

// Grain is the singleton maintenance grain. It coalesces SweepPendingAccounts
// triggers so at most one sweep runs at a time: each sweep executes in a child
// worker requested via a future, and a trigger that arrives while a sweep is
// in-flight returns accepted=false. The running flag is cleared by the future's
// reentrant continuation — on the worker's reply or on a timeout — so it can never
// stick.
type Grain struct {
	sweeper       Sweeper
	now           func() time.Time
	futureTimeout time.Duration
	dbTimeout     time.Duration
	running       bool
}

// Option overrides a Grain default.
type Option func(*Grain)

// WithTimeouts overrides the grain's wait for a worker (futureTimeout) and the
// worker's database-call timeout (dbTimeout). Keep dbTimeout shorter than
// futureTimeout so a slow sweep can report its own timeout before the actor future
// expires. Tests use short values.
func WithTimeouts(futureTimeout, dbTimeout time.Duration) Option {
	return func(g *Grain) {
		g.futureTimeout = futureTimeout
		g.dbTimeout = dbTimeout
	}
}

// stopWorkerSupervisor stops a sweep worker on any failure rather than applying
// protoactor's default restart. A restarted worker would sit idle — it already
// consumed its one runSweep message — and, because the grain never passivates,
// such idle workers would accumulate. Stopping it instead lets the request future
// time out and clear running. (protoactor resolves a child's supervisor from its
// parent's props.)
var stopWorkerSupervisor = actor.NewOneForOneStrategy(0, 0, func(any) actor.Directive {
	return actor.StopDirective
})

// NewKind builds the cluster.Kind for the maintenance grain. sweeper performs the
// database work each trigger runs and is required.
func NewKind(sweeper Sweeper, opts ...Option) *cluster.Kind {
	if sweeper == nil {
		// Fail at wiring time with a clear message rather than as an opaque nil
		// interface panic inside the first worker's sweep.
		panic("maintenance: nil Sweeper")
	}

	cfg := &Grain{
		futureTimeout: sweepFutureTimeout,
		dbTimeout:     sweepDBTimeout,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	validateTimeouts(cfg.futureTimeout, cfg.dbTimeout)

	return maintenancepb.NewMaintenanceGrainKind(
		func() maintenancepb.MaintenanceGrain {
			return &Grain{
				sweeper:       sweeper,
				now:           time.Now,
				futureTimeout: cfg.futureTimeout,
				dbTimeout:     cfg.dbTimeout,
			}
		},
		passivationTimeout,
		actor.WithReceiverMiddleware(middleware.GrainLogging(kindName)),
		actor.WithSupervisor(stopWorkerSupervisor),
	)
}

func validateTimeouts(futureTimeout, dbTimeout time.Duration) {
	switch {
	case futureTimeout <= 0:
		panic("maintenance: future timeout must be positive")
	case dbTimeout <= 0:
		panic("maintenance: DB timeout must be positive")
	case dbTimeout >= futureTimeout:
		panic("maintenance: DB timeout must be shorter than future timeout")
	}
}

// Init and Terminate have nothing to set up or tear down: the grain holds no
// activation-scoped resources, only the transient per-sweep worker.
func (g *Grain) Init(cluster.GrainContext)      {}
func (g *Grain) Terminate(cluster.GrainContext) {}

// SweepPendingAccounts starts a sweep in a child worker unless one is already
// running, and returns immediately. The worker replies over a request future; a
// reentrant continuation (running on the grain's own context) clears running and
// logs the outcome — so the caller never blocks on the sweep, and the flag clears
// whether the worker replies, fails, or times out.
func (g *Grain) SweepPendingAccounts(_ *maintenancepb.SweepPendingAccountsRequest, ctx cluster.GrainContext) (*maintenancepb.SweepPendingAccountsResponse, error) {
	if g.running {
		return &maintenancepb.SweepPendingAccountsResponse{Accepted: false}, nil
	}
	g.running = true
	worker := ctx.Spawn(actor.PropsFromProducer(func() actor.Actor {
		return newSweepWorker(g.sweeper, g.now, g.dbTimeout)
	}))
	future := ctx.RequestFuture(worker, runSweep{}, g.futureTimeout)
	ctx.ReenterAfter(future, func(res any, err error) {
		g.running = false
		g.logOutcome(ctx, res, err)
	})
	return &maintenancepb.SweepPendingAccountsResponse{Accepted: true}, nil
}

// logOutcome reports a finished sweep. err is set when the future failed (the worker
// hung past the timeout or crashed); otherwise res carries the worker's reply.
func (g *Grain) logOutcome(ctx cluster.GrainContext, res any, err error) {
	if err != nil {
		slog.Error("maintenance.pending_account_gc.unfinished",
			"grain_id", ctx.Identity(), "error", err)
		return
	}
	result, ok := res.(sweepResult)
	if !ok {
		slog.Error("maintenance.pending_account_gc.unexpected_reply",
			"grain_id", ctx.Identity(), "reply_type", fmt.Sprintf("%T", res))
		return
	}
	if result.err != nil {
		slog.Error("maintenance.pending_account_gc.failed",
			"grain_id", ctx.Identity(), "error", result.err)
		return
	}
	slog.Info("maintenance.pending_account_gc.swept",
		"grain_id", ctx.Identity(), "deleted", result.deleted)
}

// ReceiveDefault logs any unexpected message. The sweep's lifecycle runs entirely
// through the request future and its reentrant continuation, so nothing routine
// arrives here.
func (g *Grain) ReceiveDefault(ctx cluster.GrainContext) {
	slog.Warn(middleware.EventGrainUnhandled,
		"grain_type", ctx.Kind(), "grain_id", ctx.Identity(),
		"msg_type", fmt.Sprintf("%T", ctx.Message()))
}

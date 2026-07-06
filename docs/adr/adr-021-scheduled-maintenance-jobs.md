# ADR-021: Scheduled maintenance jobs — an internal trigger, a singleton grain, and an advisory-lock backstop

- **Status:** Accepted
- **Date:** 2026-07-06
- **Related:** [ADR-001](adr-001-grain-topology.md), [ADR-016](adr-016-gateway-backend-tier-separation.md), [ADR-017](adr-017-supervision-strategy.md), [ADR-007](adr-007-database-authoritative-persistence.md)

## Context

Some work is periodic rather than request-driven. The first instance is
pending-account garbage collection: a registration that is never verified leaves a
`pending` account and its verification challenge behind, and those must be swept once
the challenge has been expired past a grace period. More such jobs will follow.

A periodic job in a multi-node cluster ([ADR-016](adr-016-gateway-backend-tier-separation.md))
raises the coordination question immediately: if several gateways each run their own
timer, several fire the job and concurrent sweeps race. Rather than elect one timer,
the design lets every gateway keep its own and makes the redundant triggers safe —
which also has to survive the reality that cluster membership changes: a failover can
briefly overlap two grain activations, and an operator (or a retry) can fire a trigger
twice. So the design needs a single cluster-wide *runner* even though triggers are
many, plus a guarantee that a duplicate run is harmless, not merely unlikely.

## Decision

**A maintenance job is triggered through an internal HTTP endpoint, coordinated by a
single cluster-wide singleton grain that coalesces concurrent triggers, and made
idempotent under races by a PostgreSQL transaction-scoped advisory lock in the job
itself.** The three layers are defense in depth, not redundancy for its own sake.

### The trigger — an internal endpoint

`POST /internal/jobs/pending-account-gc` is served only on the gateway's internal
listener (never the public API listener) and is unauthenticated — access is
restricted at the network layer. By default each gateway process runs a local cron
(`github.com/robfig/cron/v3`) that POSTs this endpoint on `--gc-schedule` (default
`@every 1m`); passing `--gc-schedule off` disables the local cron and hands the
cadence to an external scheduler instead. Because every gateway crons independently,
the endpoint receives redundant triggers by design — which is exactly what the
coordinator and the backstop below absorb. The handler asks the maintenance grain to
run and returns immediately: `202` when this call started a sweep, `200` when one was
already running (coalesced), `503` when the grain is unreachable. Putting the trigger
behind an endpoint — rather than calling the sweep in-process — means one contract
serves the local cron, an external scheduler, and a manual operator run alike, and the
zero-config default reclaims abandoned registrations within about a minute with no
external setup.

### The coordinator — a singleton grain

The job is a grain with a fixed cluster identity (`pending-account-gc`), so
Proto.Cluster routes every trigger to one activation — the cluster-wide coordination
point ([ADR-001](adr-001-grain-topology.md)). Passivation is disabled: the grain is
one fixed system identity, not one activation per user or room, and must not
deactivate while a worker may still be running.

- **Coalescing.** A `running` flag rejects a trigger that arrives while a sweep is
  in flight (`accepted=false`); concurrent triggers collapse to one run.
- **Non-blocking, self-healing execution.** The grain spawns a child worker and
  requests it via a future, then registers a reentrant continuation
  (`ReenterAfter`) that clears `running` and logs the outcome — on the worker's
  reply, its failure, *or* a timeout. So the trigger call never blocks on the
  database, and a hung or crashed worker can never leave the flag stuck. The
  worker's database call is bounded shorter than the future, so a slow sweep reports
  its own timeout before the actor future fires.
- **Stop, don't restart, the worker.** A custom supervisor stops a failed worker
  rather than applying the default restart ([ADR-017](adr-017-supervision-strategy.md)):
  a restarted worker would sit idle (it already consumed its one run message) and,
  because the grain never passivates, idle workers would accumulate. Stopping lets
  the future time out and clear the flag.

### The backstop — a transaction-scoped advisory lock

The sweep itself takes `pg_try_advisory_xact_lock` on a fixed key inside its
transaction before deleting. If another sweep already holds the lock, it returns
`(0, nil)` without scanning. The lock and the delete run in the same transaction, so
the lock auto-releases on commit or abort. This makes a duplicate run — from a second
scheduler, a retry, or an activation overlap during topology churn — harmless at the
database, independent of the grain's coalescing.

The sweep is also idempotent by construction: it is a set-shaped delete
(`DELETE … WHERE status = 'pending' AND challenge expired before the cutoff`), and a
`resend` extends the challenge's expiry, so an account a user is still verifying is
never swept. The verification row is removed by its `ON DELETE CASCADE`.

## Consequences

### Positive

- **One cluster-wide runner without a leader-election dependency.** The singleton
  grain *is* the coordination point the framework already provides; no external
  consensus system is introduced.
- **Duplicate triggers are safe, not merely rare.** The advisory lock and the
  set-shaped, idempotent sweep mean a race during a failover, a retried trigger, or a
  second scheduler cannot double-delete or corrupt state.
- **A stuck job cannot wedge the pipeline.** The reentrant future clears the running
  flag on reply, failure, or timeout; a hung worker times out and the next trigger
  proceeds.
- **The cadence is operationally configurable.** The zero-config default is an
  in-process gateway-local cron (`--gc-schedule @every 1m`); `--gc-schedule off` hands
  the cadence to an external scheduler; and either way the endpoint stays manually
  triggerable for testing or an incident.

### Negative

- **The internal endpoint must be network-isolated.** It is unauthenticated by
  design and relies on the internal listener not being publicly reachable; a
  misconfigured network exposure would let anyone trigger the job. (Triggering it is
  low-harm — the sweep is idempotent and self-limiting — but the isolation is a
  deployment responsibility.)
- **Every gateway's cron fires redundant triggers.** The zero-config default runs a
  timer per gateway, so the coalescing and the advisory lock do routine work on every
  tick, not only during rare races — a little wasted trigger traffic (bounded, cheap
  POSTs) in exchange for needing no external scheduler and no leader election. A
  deployment that prefers a single cadence removes the redundancy with
  `--gc-schedule off` and its own scheduler.
- **The singleton concentrates the job on one activation.** A very heavy periodic
  job would load one node; acceptable for sweeps, and the per-job identity means a
  future heavy job can be split without disturbing this one.

### Neutral

- **Each future job gets its own grain identity.** One activation per job keeps the
  per-job coalescing state from spanning unrelated jobs.
- **The advisory-lock key is arbitrary but fixed.** Every process uses the same key,
  and it only needs to stay stable and not collide with another advisory lock in the
  database.

## Alternatives considered

### A single designated scheduler node, or a leader-elected timer

Nominate one gateway (or elect a leader among them) to own the timer so the job fires
exactly once. Rejected: it adds a leader-election mechanism or a special-node role for
no gain, because the singleton grain already funnels every trigger to one runner and
the advisory lock makes any overlap harmless. Letting every gateway keep an
independent cron and coalescing the results is simpler than electing which one is
allowed to fire — and it degrades gracefully, since any surviving gateway keeps
triggering when others are down.

### An external scheduler that runs the job directly (SQL or a script)

Let cron execute the deletion itself against the database. Rejected: it couples the
scheduler to the schema, bypasses the application's supervision, logging, and grace
logic, and duplicates the sweep's rules outside the code that owns them. The internal
endpoint keeps the job's behavior in the application; the scheduler only decides
*when*.

### A leader-election library (Raft, an etcd/Consul lease)

Elect a leader that runs all periodic jobs. Rejected as disproportionate: a
consensus system is heavy machinery for a rare, idempotent sweep. A cluster-singleton
grain gives the single-runner property the framework already supports, and the
advisory lock covers the brief windows where "singleton" overlaps.

### A durable job queue with a held row lock

Enqueue jobs in a table and hold a row lock for the run's duration. Rejected: more
machinery than a periodic idempotent sweep needs, and a long-held row lock is
heavier and more failure-prone than a transaction-scoped advisory lock that
auto-releases. A durable queue earns its place when jobs need retry bookkeeping and
audit, which this does not.

## References

- [ADR-001](adr-001-grain-topology.md) — the grain model; a fixed-identity singleton
  grain is the cluster-wide coordination point.
- [ADR-016](adr-016-gateway-backend-tier-separation.md) — the gateway serves the
  trigger endpoint; the backend hosts the grain and runs the sweep.
- [ADR-017](adr-017-supervision-strategy.md) — the stop-don't-restart supervision the
  transient worker uses.
- [ADR-007](adr-007-database-authoritative-persistence.md) — the store the sweep
  deletes from, and the `ON DELETE CASCADE` that removes the verification row.
- `internal/grain/maintenance/` — the singleton grain, the child worker, and the
  reentrant continuation.
- `internal/gateway/handler_internal_jobs.go` and `maintenance_trigger.go` — the
  internal trigger endpoint and the cluster call into the grain.
- `cmd/gateway/cron.go` and `cmd/gateway/main.go` — the gateway-local `robfig/cron`
  scheduler and the `--gc-schedule` flag (default `@every 1m`, `off` to hand off).
- `internal/persistence/accountgc/sweep.go` — the advisory lock and the idempotent
  sweep.

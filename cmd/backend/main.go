// Command backend runs the blabby grain tier: a proto.actor cluster member
// hosting the User and Room grains. It serves no HTTP — the gateway
// (cmd/gateway) fronts clients and calls these grains across the cluster.
//
// It requires a reachable PostgreSQL: grains hydrate room/membership state from
// it on activation, and the backend acquires a worker-id lease at startup to mint
// event ids — so it fails fast if the database is down. The connection defaults to
// the local dev DSN; override with --db-dsn or BLABBY_DATABASE_URL. Start one with
// `docker compose up -d postgres` (see README / docker-compose.yml).
//
// Run with
//
//	go run ./cmd/backend
//
// and it joins as a single member. Supplying one or more discovery --seeds forms a
// multi-node cluster; multi-node mode additionally requires an explicit,
// peer-reachable --advertised-host and a fixed --cluster-port. See
// docs/multi-node-cluster.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oklahomer/blabby/internal/clusterboot"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/logging"
	"github.com/oklahomer/blabby/internal/persistence/accountgc"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/persistence/workerlease"
)

// pendingAccountGCGrace is how long after a verification challenge expires an
// unverified pending account is kept before the maintenance grain sweeps it. It is
// short so abandoned registrations are reclaimed quickly in the demo; a resend
// extends the challenge's expiry, so an account a user is still verifying is safe.
const pendingAccountGCGrace = 5 * time.Minute

func main() {
	level := logging.SetupDefault()
	slog.Info("server.startup", "log_level", level.String())

	dbCfg, cc, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if err := run(dbCfg, cc); err != nil {
		slog.Error("server.fatal", "error", err)
		os.Exit(1)
	}
}

// parseConfig parses the backend's command-line flags — database connection and
// cluster bootstrap — into validated configuration. It uses a dedicated FlagSet
// (not the global flag.CommandLine) so it can be driven directly from tests; the
// database flags are registered by postgres on the same FlagSet.
func parseConfig(args []string) (postgres.Config, clusterboot.Config, error) {
	fs := flag.NewFlagSet("blabby-backend", flag.ContinueOnError)
	dbCfg := postgres.BindFlags(fs)
	clusterCfg := clusterboot.BindFlags(fs, clusterboot.MemberDefaults())
	if err := fs.Parse(args); err != nil {
		return postgres.Config{}, clusterboot.Config{}, err
	}
	dc, err := dbCfg()
	if err != nil {
		return postgres.Config{}, clusterboot.Config{}, err
	}
	cc, err := clusterCfg()
	if err != nil {
		return postgres.Config{}, clusterboot.Config{}, err
	}
	return dc, cc, nil
}

// run starts the cluster member and blocks until a shutdown signal arrives,
// then stops the cluster. The backend hosts grains only; there is no HTTP
// server to drain.
func run(dbCfg postgres.Config, cc clusterboot.Config) error {
	for _, w := range cc.Warnings() {
		slog.Warn("server.cluster.config_warning", "detail", w)
	}

	// The grains hydrate room/membership state from the database on activation,
	// so the backend holds a pool. It fails closed: an unreachable database stops
	// startup rather than surfacing on the first activation.
	pool, err := postgres.NewPool(context.Background(), dbCfg)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	// The backend is now an id-minting node: a membership transition appends a
	// timeline event whose id is a Snowflake. The worker-lease manager acquires a
	// fenced worker id at boot (from the worker_lease table on this same pool) and
	// mints only while it holds the lease, so two members never share a worker id.
	leaseManager, err := workerlease.NewManager(workerlease.NewRepo(pool), workerlease.HostPIDOwner())
	if err != nil {
		return fmt.Errorf("build worker-lease manager: %w", err)
	}
	if err := leaseManager.Start(context.Background()); err != nil {
		return fmt.Errorf("acquire worker lease: %w", err)
	}
	defer leaseManager.Stop()

	// The singleton maintenance grain runs the pending-account GC over this same
	// pool. The grace window is short so abandoned registrations are reclaimed
	// quickly; a resend extends a challenge's expiry and so protects an account a
	// user is still verifying.
	sweeper, err := accountgc.NewSweeper(postgres.NewTransactor(pool), pendingAccountGCGrace)
	if err != nil {
		return fmt.Errorf("build pending-account sweeper: %w", err)
	}

	// The User grain's display-name directory reads service_user via userrepo over
	// this same pool, so every member resolves identical UserRefs from the one
	// shared source.
	deps := clusterboot.GrainDeps{
		Directory:   user.NewRepoDirectory(pool),
		RoomLoader:  room.NewRoomRepoLoader(pool),
		Membership:  room.NewMembershipStore(pool, leaseManager),
		JoinedRooms: user.NewJoinedRoomLoader(pool),
		Sweeper:     sweeper,
	}

	c := clusterboot.Build(cc, clusterboot.Kinds(deps)...)
	defer c.Shutdown(true)

	// Subscribe before StartMember so the initial join topology is not missed.
	sub := clusterboot.SubscribeTopologyLogging(c)
	defer c.ActorSystem.EventStream.Unsubscribe(sub)

	c.StartMember()

	// Address is the peer-reachable address protoactor advertises; logging it
	// after StartMember lets an operator confirm what peers (and gateways) see.
	slog.Info("server.cluster.started", "advertised_address", c.ActorSystem.Address())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	slog.Info("server.shutdown", "reason", "signal")
	return nil
}

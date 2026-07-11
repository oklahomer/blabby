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
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/oklahomer/blabby/internal/clusterboot"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/logging"
	"github.com/oklahomer/blabby/internal/persistence/accountgc"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/persistence/workerlease"
	"github.com/oklahomer/blabby/internal/telemetry"
)

// pendingAccountGCGrace is how long after a verification challenge expires an
// unverified pending account is kept before the maintenance grain sweeps it. It is
// short so abandoned registrations are reclaimed quickly in the demo; a resend
// extends the challenge's expiry, so an account a user is still verifying is safe.
const pendingAccountGCGrace = 5 * time.Minute

const (
	// readHeaderTimeout bounds how long a client may take to send request
	// headers on the metrics listener — a Slowloris guard.
	readHeaderTimeout = 5 * time.Second

	// metricsShutdownTimeout bounds the graceful drain of the metrics server and
	// the MeterProvider flush on SIGINT/SIGTERM.
	metricsShutdownTimeout = 5 * time.Second
)

// config is the backend's parsed, validated non-database configuration.
// Constructing it via newConfig is the single boundary where raw flag strings
// are checked. The cluster settings live separately in clusterboot.Config.
type config struct {
	// metricsListen is the dedicated operational listener for GET /metrics
	// (host:port). Empty disables the metrics server.
	metricsListen string
}

func main() {
	level := logging.SetupDefault()
	slog.Info("server.startup", "log_level", level.String())

	cfg, dbCfg, cc, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if err := run(cfg, dbCfg, cc); err != nil {
		slog.Error("server.fatal", "error", err)
		os.Exit(1)
	}
}

// parseConfig parses the backend's command-line flags — database connection,
// cluster bootstrap, and the optional metrics listener — into validated
// configuration. It uses a dedicated FlagSet (not the global flag.CommandLine)
// so it can be driven directly from tests; the database flags are registered by
// postgres on the same FlagSet.
func parseConfig(args []string) (config, postgres.Config, clusterboot.Config, error) {
	fs := flag.NewFlagSet("blabby-backend", flag.ContinueOnError)
	metricsListen := fs.String("metrics-listen", "", "operational listener (host:port) serving GET /metrics; empty disables it — keep network-restricted")
	dbCfg := postgres.BindFlags(fs)
	clusterCfg := clusterboot.BindFlags(fs, clusterboot.MemberDefaults())
	if err := fs.Parse(args); err != nil {
		return config{}, postgres.Config{}, clusterboot.Config{}, err
	}
	cfg, err := newConfig(*metricsListen)
	if err != nil {
		return config{}, postgres.Config{}, clusterboot.Config{}, err
	}
	dc, err := dbCfg()
	if err != nil {
		return config{}, postgres.Config{}, clusterboot.Config{}, err
	}
	cc, err := clusterCfg()
	if err != nil {
		return config{}, postgres.Config{}, clusterboot.Config{}, err
	}
	return cfg, dc, cc, nil
}

// newConfig validates the raw --metrics-listen value into a config (parse,
// don't validate): empty (the default) disables the metrics server; a non-empty
// value must be a well-formed host:port, kept verbatim.
func newConfig(metricsListen string) (config, error) {
	addr := strings.TrimSpace(metricsListen)
	if addr == "" {
		return config{}, nil
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return config{}, fmt.Errorf("--metrics-listen %q is not a valid host:port: %w", addr, err)
	}
	return config{metricsListen: addr}, nil
}

// metricsMux builds the handler for the backend's dedicated metrics listener:
// GET /metrics served by h, a 405 for other methods on that path, and a 404
// catch-all. It is a function so tests can exercise the routing without binding
// a port.
func metricsMux(h http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", h)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	return mux
}

// run starts the cluster member and blocks until a shutdown signal arrives,
// then stops the cluster. The backend hosts grains only; its sole HTTP surface
// is the optional metrics listener (--metrics-listen), which is drained before
// return.
func run(cfg config, dbCfg postgres.Config, cc clusterboot.Config) error {
	for _, w := range cc.Warnings() {
		slog.Warn("server.cluster.config_warning", "detail", w)
	}

	// Metrics are opt-in (--metrics-listen). When enabled, one MeterProvider
	// feeds proto.actor's instrumentation and its paired scrape handler is served
	// on a dedicated operational listener; the provider is flushed on shutdown.
	// When disabled, tel stays the zero value and no listener is opened.
	var tel clusterboot.Telemetry
	var metrics *telemetry.PrometheusMetrics
	if cfg.metricsListen != "" {
		var err error
		metrics, err = telemetry.NewPrometheusMetrics()
		if err != nil {
			return fmt.Errorf("build telemetry: %w", err)
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), metricsShutdownTimeout)
			defer cancel()
			if err := metrics.Shutdown(shutdownCtx); err != nil {
				slog.Warn("server.telemetry.shutdown_error", "error", err)
			}
		}()
		tel = clusterboot.Telemetry{MeterProvider: metrics.MeterProvider()}
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

	// The User grain's display-name directory reads service_user via the persistence user repo over
	// this same pool, so every member resolves identical UserRefs from the one
	// shared source.
	deps := clusterboot.GrainDeps{
		Directory:   user.NewRepoDirectory(pool),
		RoomLoader:  room.NewRoomRepoLoader(pool),
		Membership:  room.NewMembershipStore(pool, leaseManager),
		Messages:    room.NewMessageStore(pool, leaseManager),
		JoinedRooms: user.NewJoinedRoomLoader(pool),
		Sweeper:     sweeper,
	}

	c := clusterboot.Build(cc, tel, clusterboot.Kinds(deps)...)
	defer c.Shutdown(true)

	// Subscribe before StartMember so the initial join topology is not missed.
	sub := clusterboot.SubscribeTopologyLogging(c)
	defer c.ActorSystem.EventStream.Unsubscribe(sub)

	// Subscribe before StartMember so no dead letter published during startup
	// is missed.
	dlSub := clusterboot.SubscribeDeadLetterLogging(c)
	defer c.ActorSystem.EventStream.Unsubscribe(dlSub)

	c.StartMember()

	// Address is the peer-reachable address protoactor advertises; logging it
	// after StartMember lets an operator confirm what peers (and gateways) see.
	slog.Info("server.cluster.started", "advertised_address", c.ActorSystem.Address())

	// The metrics server, when enabled, starts after StartMember so proto.actor's
	// instrumentation is live before the endpoint is scrapeable. A bind failure on
	// the explicitly requested port is fatal, mirroring the gateway's servers.
	var metricsSrv *http.Server
	serveErr := make(chan error, 1)
	if metrics != nil {
		metricsSrv = &http.Server{
			Addr:              cfg.metricsListen,
			Handler:           metricsMux(metrics.HTTPHandler()),
			ReadHeaderTimeout: readHeaderTimeout,
		}
		go func() {
			slog.Info("server.http.listening", "addr", metricsSrv.Addr)
			err := metricsSrv.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			serveErr <- err
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("metrics server: %w", err)
		}
		return nil
	case <-ctx.Done():
		slog.Info("server.shutdown", "reason", "signal")
	}

	if metricsSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), metricsShutdownTimeout)
		defer cancel()
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("metrics shutdown: %w", err)
		}
	}
	return nil
}

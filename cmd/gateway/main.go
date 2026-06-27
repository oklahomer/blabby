// Command gateway runs the blabby API tier: an HTTP + WebSocket front end that
// joins the proto.actor cluster as a client and routes requests to the User and
// Room grains hosted by the backend (cmd/backend). It hosts the per-connection
// UserConnection actors locally but never hosts grains itself.
//
// Run with
//
//	go run ./cmd/gateway --seeds <backend-host:discoveryPort> \
//	    --advertised-host <host:cluster-port> --cluster-port <port>
//
// It serves HTTP on :8080 by default. Override the listen address with --listen
// and the JWT signing secret with --jwt-secret; when no secret is supplied a
// built-in development key is used (and a warning is logged). Every gateway in a
// real deployment MUST share the same signing secret. See
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

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/clusterboot"
	"github.com/oklahomer/blabby/internal/gateway"
	"github.com/oklahomer/blabby/internal/logging"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/persistence/workerlease"
)

const (
	defaultListenAddr = ":8080"

	// devJWTSecret is the fallback signing key used when --jwt-secret is not
	// provided, so a fresh clone runs with zero configuration. It is
	// intentionally well-known and MUST NOT be used in any real deployment.
	devJWTSecret = "blabby-dev-insecure-signing-key"

	// readHeaderTimeout bounds how long a client may take to send request
	// headers — a Slowloris guard. It does not apply to the request body or to
	// the long-lived WebSocket connection, so /ws is unaffected.
	readHeaderTimeout = 5 * time.Second

	// shutdownTimeout bounds graceful HTTP drain on SIGINT/SIGTERM.
	shutdownTimeout = 10 * time.Second

	// Gateway cluster demo defaults make `go run ./cmd/gateway` join a local
	// single-node backend (on its default discovery port) as a client, so the
	// Quick Start needs no flags. A real deployment overrides --seeds,
	// --advertised-host, and --cluster-port. The advertised host is loopback, so
	// the gateway logs the expected reachability warning at startup.
	defaultGatewayClusterHost   = "127.0.0.1"
	defaultGatewayClusterPort   = 8091
	defaultGatewayDiscoveryPort = 6331
	defaultGatewaySeeds         = "127.0.0.1:6330"
)

// gatewayClusterDefaults are the cluster-flag defaults for a flagless local
// demo: the gateway joins a loopback backend as a client. The advertised host
// matches the bind port so peers reach the gateway where it actually listens.
func gatewayClusterDefaults() clusterboot.Defaults {
	return clusterboot.Defaults{
		ClusterHost:    defaultGatewayClusterHost,
		ClusterPort:    defaultGatewayClusterPort,
		AdvertisedHost: fmt.Sprintf("%s:%d", defaultGatewayClusterHost, defaultGatewayClusterPort),
		DiscoveryPort:  defaultGatewayDiscoveryPort,
		Seeds:          defaultGatewaySeeds,
	}
}

// config is the parsed, validated HTTP/auth configuration. Constructing it via
// newConfig is the single boundary where raw flag strings are checked. The
// cluster settings live separately in clusterboot.Config.
type config struct {
	listenAddr     string
	jwtSecret      string
	usingDevSecret bool
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

// parseConfig parses the gateway's command-line flags into validated HTTP/auth
// and cluster configuration. It uses a dedicated FlagSet so it can be driven
// directly from tests; the cluster flags are registered by clusterboot on the
// same FlagSet.
func parseConfig(args []string) (config, postgres.Config, clusterboot.Config, error) {
	fs := flag.NewFlagSet("blabby-gateway", flag.ContinueOnError)
	listen := fs.String("listen", defaultListenAddr, "HTTP listen address (host:port)")
	secret := fs.String("jwt-secret", "", "JWT signing secret; a development default is used when empty")
	dbCfg := postgres.BindFlags(fs)
	clusterCfg := clusterboot.BindFlags(fs, gatewayClusterDefaults())
	if err := fs.Parse(args); err != nil {
		return config{}, postgres.Config{}, clusterboot.Config{}, err
	}

	cfg, err := newConfig(*listen, *secret)
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

// newConfig validates raw flag values into a config (parse, don't validate).
// The listen address must be a well-formed host:port. An empty signing secret
// falls back to devJWTSecret and flags usingDevSecret so run can warn.
func newConfig(listen, secret string) (config, error) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return config{}, errors.New("--listen must not be empty")
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return config{}, fmt.Errorf("--listen %q is not a valid host:port: %w", listen, err)
	}

	cfg := config{listenAddr: listen}
	if s := strings.TrimSpace(secret); s != "" {
		cfg.jwtSecret = s
	} else {
		cfg.jwtSecret = devJWTSecret
		cfg.usingDevSecret = true
	}
	return cfg, nil
}

// run joins the cluster as a client and serves the HTTP gateway, then blocks
// until a shutdown signal arrives. On shutdown it drains HTTP first (serve) and
// then leaves the cluster (deferred), so in-flight requests finish before the
// client's transport goes away.
func run(cfg config, dbCfg postgres.Config, cc clusterboot.Config) error {
	if cfg.usingDevSecret {
		slog.Warn("server.jwt_secret.dev_default",
			"detail", "using built-in development JWT secret; set --jwt-secret for any real deployment")
	}
	for _, w := range cc.Warnings() {
		slog.Warn("server.cluster.config_warning", "detail", w)
	}

	// The gateway holds its own read pool to resolve client-facing room codes (R…)
	// to internal RoomIDs. It reads rooms but never mints them, so the room repo's
	// id source is unused.
	pool, err := postgres.NewPool(context.Background(), dbCfg)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	roomDir := gateway.NewRoomRepoDirectory(pool)

	// The gateway is a Snowflake id-minting tier alongside the backend: account
	// registration, fronted by the gateway, mints a new UserID and public_code.
	// The worker-lease manager acquires a fenced worker id at boot from the
	// worker_lease table on this same pool and mints only while it holds the
	// lease, so the gateway and backend never share a worker id. Stop is deferred
	// after pool.Close so releasing the lease (which uses the pool) runs first.
	leaseManager, err := workerlease.NewManager(workerlease.NewRepo(pool), workerlease.HostPIDOwner())
	if err != nil {
		return fmt.Errorf("build worker-lease manager: %w", err)
	}
	if err := leaseManager.Start(context.Background()); err != nil {
		return fmt.Errorf("acquire worker lease: %w", err)
	}
	defer leaseManager.Stop()

	// The user directory backs login (verify email+password) and token validation
	// (resolve the U… subject to a UserID), both over the gateway's pool. One value
	// satisfies both auth collaborators, so it is passed as verifier and resolver.
	userDir := gateway.NewUserRepoDirectory(pool)

	// The gateway joins as a cluster client: it registers no grain kinds (a
	// client routes to grains via the topology that members advertise) and never
	// hosts an activation.
	c := clusterboot.Build(cc)
	defer clusterboot.ShutdownClient(c)

	// Subscribe before StartClient so the initial cluster topology is not missed.
	sub := clusterboot.SubscribeTopologyLogging(c)
	defer c.ActorSystem.EventStream.Unsubscribe(sub)

	c.StartClient()

	// Address is what backends use to reach this gateway's UserConnection PIDs
	// for fan-out; logging it after StartClient lets an operator confirm it.
	slog.Info("server.cluster.started", "advertised_address", c.ActorSystem.Address())

	authenticator := auth.NewJWTAuthenticator([]byte(cfg.jwtSecret), userDir, userDir)
	gw := gateway.NewGateway(authenticator, roomDir, c, c.ActorSystem.Root)

	srv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           gw.RegisterRoutes(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	return serve(srv)
}

// serve runs srv until SIGINT/SIGTERM, then drains it gracefully. A listen
// failure (e.g. the port is already in use) is returned before any signal so
// the process exits non-zero; a signal-initiated shutdown returns nil.
func serve(srv *http.Server) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("server.http.listening", "addr", srv.Addr)
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		slog.Info("server.shutdown", "reason", "signal")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

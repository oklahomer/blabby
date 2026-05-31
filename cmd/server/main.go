// Command server runs the blabby chat backend: a single-node proto.actor
// cluster hosting the User and Room grains, fronted by the HTTP + WebSocket
// gateway. Run with
//
//	go run ./cmd/server
//
// and it listens on :8080 with zero external dependencies. Override the
// listen address with --listen and the JWT signing secret with --jwt-secret;
// when no secret is supplied a built-in development key is used (and a warning
// is logged) so a fresh clone runs without configuration.
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

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/cluster/clusterproviders/automanaged"
	"github.com/asynkron/protoactor-go/cluster/identitylookup/disthash"
	"github.com/asynkron/protoactor-go/remote"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/gateway"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/logging"
)

const (
	defaultListenAddr = ":8080"
	clusterName       = "blabby"

	// devJWTSecret is the fallback signing key used when --jwt-secret is
	// not provided, so a fresh clone runs with zero configuration. It is
	// intentionally well-known and MUST NOT be used in any real deployment.
	devJWTSecret = "blabby-dev-insecure-signing-key"

	// readHeaderTimeout bounds how long a client may take to send request
	// headers — a Slowloris guard. It does not apply to the request body or
	// to the long-lived WebSocket connection, so /ws is unaffected.
	readHeaderTimeout = 5 * time.Second

	// shutdownTimeout bounds graceful HTTP drain on SIGINT/SIGTERM.
	shutdownTimeout = 10 * time.Second
)

// config is the parsed, validated server configuration. Constructing it via
// newConfig is the single boundary where raw flag strings are checked, so the
// rest of the program can trust these values.
type config struct {
	listenAddr     string
	jwtSecret      string
	usingDevSecret bool
}

func main() {
	level := logging.SetupDefault()
	slog.Info("server.startup", "log_level", level.String())

	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if err := run(cfg); err != nil {
		slog.Error("server.fatal", "error", err)
		os.Exit(1)
	}
}

// parseConfig parses the server's command-line flags into a validated config.
// It uses a dedicated FlagSet (not the global flag.CommandLine) so it can be
// driven directly from tests.
func parseConfig(args []string) (config, error) {
	fs := flag.NewFlagSet("blabby-server", flag.ContinueOnError)
	listen := fs.String("listen", defaultListenAddr, "HTTP listen address (host:port)")
	secret := fs.String("jwt-secret", "", "JWT signing secret; a development default is used when empty")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	return newConfig(*listen, *secret)
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

// clusterKinds returns the grain kinds the server hosts. It is separated from
// bootstrapCluster so a test can assert the registered kinds without standing
// up an actor system.
func clusterKinds() []*cluster.Kind {
	return []*cluster.Kind{user.NewKind(), room.NewKind()}
}

// bootstrapCluster builds (but does not start) a single-node proto.actor
// cluster hosting the User and Room grains. The remote transport binds an
// OS-assigned ephemeral port on loopback: a single node never accepts inbound
// peer connections, so the port is not load-bearing. automanaged.New supplies
// the discovery provider with its defaults (2s refresh, port 6330).
//
// cluster.Config.RequestLog is intentionally left at its false default: the
// built-in RequestLog formatter logs whole proto request bodies via slog.Any,
// which would leak message text and bearer tokens into the log stream.
func bootstrapCluster() *cluster.Cluster {
	system := actor.NewActorSystem()
	remoteCfg := remote.Configure("127.0.0.1", 0)
	provider := automanaged.New()
	lookup := disthash.New()

	cfg := cluster.Configure(
		clusterName,
		provider,
		lookup,
		remoteCfg,
		cluster.WithKinds(clusterKinds()...),
	)
	return cluster.New(system, cfg)
}

// run starts the cluster member and HTTP gateway, then blocks until a
// shutdown signal arrives. On shutdown it drains HTTP first (serve) and then
// stops the cluster (deferred), so in-flight requests finish before the grains
// they depend on go away.
func run(cfg config) error {
	if cfg.usingDevSecret {
		slog.Warn("server.jwt_secret.dev_default",
			"detail", "using built-in development JWT secret; set --jwt-secret for any real deployment")
	}

	c := bootstrapCluster()
	c.StartMember()
	defer c.Shutdown(true)

	// Address is the peer-reachable address protoactor advertises; logging it
	// after StartMember (the remote layer is up by then) lets an operator
	// confirm what peers would see. Before StartMember it is the placeholder
	// "nonhost".
	slog.Info("server.cluster.started", "advertised_address", c.ActorSystem.Address())

	authenticator := auth.NewJWTAuthenticator([]byte(cfg.jwtSecret), auth.NewInMemoryUserStore())
	gw := gateway.NewGateway(authenticator, c, c.ActorSystem.Root)

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

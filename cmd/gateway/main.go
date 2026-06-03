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
)

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

	cfg, cc, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if err := run(cfg, cc); err != nil {
		slog.Error("server.fatal", "error", err)
		os.Exit(1)
	}
}

// parseConfig parses the gateway's command-line flags into validated HTTP/auth
// and cluster configuration. It uses a dedicated FlagSet so it can be driven
// directly from tests; the cluster flags are registered by clusterboot on the
// same FlagSet.
func parseConfig(args []string) (config, clusterboot.Config, error) {
	fs := flag.NewFlagSet("blabby-gateway", flag.ContinueOnError)
	listen := fs.String("listen", defaultListenAddr, "HTTP listen address (host:port)")
	secret := fs.String("jwt-secret", "", "JWT signing secret; a development default is used when empty")
	clusterCfg := clusterboot.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		return config{}, clusterboot.Config{}, err
	}

	cfg, err := newConfig(*listen, *secret)
	if err != nil {
		return config{}, clusterboot.Config{}, err
	}
	cc, err := clusterCfg()
	if err != nil {
		return config{}, clusterboot.Config{}, err
	}
	return cfg, cc, nil
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
func run(cfg config, cc clusterboot.Config) error {
	if cfg.usingDevSecret {
		slog.Warn("server.jwt_secret.dev_default",
			"detail", "using built-in development JWT secret; set --jwt-secret for any real deployment")
	}
	for _, w := range cc.Warnings() {
		slog.Warn("server.cluster.config_warning", "detail", w)
	}

	// The in-memory store backs credential lookup for login and token issue.
	// The gateway never resolves display names — that is the User grain's job —
	// so it consumes the store only as an auth.UserStore.
	store := auth.NewInMemoryUserStore()

	// Kinds are registered for grain-call routing; the gateway is a client and
	// never activates a grain, so it passes a nil directory.
	c := clusterboot.Build(cc, clusterboot.Kinds(nil)...)
	defer c.Shutdown(true)

	// Subscribe before StartClient so the initial cluster topology is not missed.
	sub := clusterboot.SubscribeTopologyLogging(c)
	defer c.ActorSystem.EventStream.Unsubscribe(sub)

	c.StartClient()

	// Address is what backends use to reach this gateway's UserConnection PIDs
	// for fan-out; logging it after StartClient lets an operator confirm it.
	slog.Info("server.cluster.started", "advertised_address", c.ActorSystem.Address())

	authenticator := auth.NewJWTAuthenticator([]byte(cfg.jwtSecret), store)
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

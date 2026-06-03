// Command backend runs the blabby grain tier: a proto.actor cluster member
// hosting the User and Room grains. It serves no HTTP — the gateway
// (cmd/gateway) fronts clients and calls these grains across the cluster.
//
// Run with
//
//	go run ./cmd/backend
//
// and it joins as a single self-contained member with zero external
// dependencies. Supplying one or more discovery --seeds forms a multi-node
// cluster; multi-node mode additionally requires an explicit, peer-reachable
// --advertised-host and a fixed --cluster-port. See docs/multi-node-cluster.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/clusterboot"
	"github.com/oklahomer/blabby/internal/logging"
)

func main() {
	level := logging.SetupDefault()
	slog.Info("server.startup", "log_level", level.String())

	cc, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if err := run(cc); err != nil {
		slog.Error("server.fatal", "error", err)
		os.Exit(1)
	}
}

// parseConfig parses the backend's command-line flags — cluster bootstrap only —
// into a validated clusterboot.Config. It uses a dedicated FlagSet (not the
// global flag.CommandLine) so it can be driven directly from tests.
func parseConfig(args []string) (clusterboot.Config, error) {
	fs := flag.NewFlagSet("blabby-backend", flag.ContinueOnError)
	clusterCfg := clusterboot.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		return clusterboot.Config{}, err
	}
	return clusterCfg()
}

// run starts the cluster member and blocks until a shutdown signal arrives,
// then stops the cluster. The backend hosts grains only; there is no HTTP
// server to drain.
func run(cc clusterboot.Config) error {
	for _, w := range cc.Warnings() {
		slog.Warn("server.cluster.config_warning", "detail", w)
	}

	// The in-memory store backs the User grain's display-name directory. Its
	// fixed, immutable seed makes every member resolve identical UserRefs, so no
	// shared store is needed across members.
	store := auth.NewInMemoryUserStore()

	c := clusterboot.Build(cc, clusterboot.Kinds(store)...)
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

package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/oklahomer/blabby/internal/logging"
)

func main() {
	level := logging.SetupDefault()
	slog.Info("server.startup", "log_level", level.String())

	// TODO(cluster bootstrap): wire the production cluster here.
	//
	//   1. Refuse to start with an empty remote.Config.AdvertisedHost.
	//      The fallback (lis.Addr().String()) is unsafe under 0.0.0.0
	//      binds, containers, or NAT — peer nodes silently dead-letter
	//      every fan-out to a registered PID. A dev/single-node escape
	//      hatch is acceptable, but the default must not auto-derive
	//      from the listener. See docs/adr/adr-011.
	//   2. Register both grain kinds:
	//        cluster.WithKinds(user.NewKind(), room.NewKind()).
	//   3. Set cluster.Config.RequestLog = false explicitly even though
	//      it defaults to false — surfacing the choice is the point.
	//      The cluster's built-in RequestLog formatter dumps full proto
	//      bodies (including message text and tokens) via slog.Any.
	//   4. HTTP server lifecycle: bind, serve, graceful shutdown on
	//      SIGTERM, log the resolved advertised address at startup.

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigs
	slog.Info("server.shutdown", "signal", sig.String())
}

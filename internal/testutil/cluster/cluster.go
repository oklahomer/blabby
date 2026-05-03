// Package clustertest provides a thin bootstrap helper for in-process
// proto.actor cluster integration tests.
//
// The Start function is for in-process integration tests only; do not use it
// in production wiring. Production cluster startup happens in cmd/server/main.go
// (introduced in a later story).
package clustertest

import (
	"net"
	"strconv"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/cluster/clusterproviders/automanaged"
	"github.com/asynkron/protoactor-go/cluster/identitylookup/disthash"
	"github.com/asynkron/protoactor-go/remote"
)

// TB is the subset of testing.TB used by Start. *testing.T satisfies it,
// and a TestMain helper can satisfy it with a tiny shim — letting tests
// share a single cluster across the whole package via TestMain when the
// alternative (one cluster per test) would race against protoactor's
// process-global grpclog state.
type TB interface {
	Helper()
	Cleanup(func())
	Fatalf(format string, args ...any)
}

// Start brings up a single-member in-process cluster suitable for unit and
// integration tests. It registers tb.Cleanup so the cluster is shut down
// automatically when the test (or TestMain) ends.
//
// Random ports are chosen for both the remote transport and the automanaged
// discovery endpoint so parallel test packages do not collide.
//
// Start is for in-process integration tests only; do not use it in production
// wiring.
func Start(tb TB, kinds ...*cluster.Kind) *cluster.Cluster {
	tb.Helper()

	autoPort, err := freeTCPPort()
	if err != nil {
		tb.Fatalf("failed to find free port for automanaged: %v", err)
	}

	system := actor.NewActorSystem()
	remoteCfg := remote.Configure("127.0.0.1", 0)
	provider := automanaged.NewWithConfig(
		2*time.Second,
		autoPort,
		net.JoinHostPort("127.0.0.1", strconv.Itoa(autoPort)),
	)
	lookup := disthash.New()

	cfg := cluster.Configure(
		"blabby-test",
		provider,
		lookup,
		remoteCfg,
		cluster.WithKinds(kinds...),
		cluster.WithRequestTimeout(2*time.Second),
	)

	c := cluster.New(system, cfg)
	c.StartMember()

	tb.Cleanup(func() { c.Shutdown(true) })

	return c
}

// freeTCPPort asks the kernel for an unused TCP port on the loopback
// interface. There is a small TOCTOU window between releasing the listener
// and the cluster binding the port; for in-process tests this is acceptable.
func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

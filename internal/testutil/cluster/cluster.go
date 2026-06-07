// Package clustertest provides a thin bootstrap helper for in-process
// proto.actor cluster integration tests.
//
// The Start function is for in-process integration tests only; do not use it
// in production wiring. Production cluster startup lives in internal/clusterboot.
package clustertest

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/cluster/clusterproviders/automanaged"
	"github.com/asynkron/protoactor-go/cluster/identitylookup/disthash"
	"github.com/asynkron/protoactor-go/remote"
)

// automanagedRefreshTTL is the gossip cycle interval passed to
// automanaged.NewWithConfig. After the provider's /_health endpoint is
// reachable, the next monitorStatuses cycle will register the local
// member in MemberList; waiting one full TTL guarantees that cycle has
// run.
const automanagedRefreshTTL = 2 * time.Second

// healthReadyTimeout bounds how long Start waits for the automanaged
// provider's HTTP /_health endpoint to bind. On slow CI runners the
// listener goroutine may not be scheduled for several hundred ms after
// StartMember returns; 15s leaves comfortable headroom while still
// failing fast on a genuinely broken bootstrap.
const healthReadyTimeout = 15 * time.Second

// healthReadyPoll is the interval between /_health probes while waiting.
const healthReadyPoll = 50 * time.Millisecond

// gossipSettleBuffer is the slack added to one refresh cycle to absorb
// timing jitter on shared CI runners between /_health becoming reachable
// and monitorStatuses publishing the local member.
const gossipSettleBuffer = 500 * time.Millisecond

// TB is the subset of testing.TB used by Start. *testing.T satisfies it,
// and a TestMain helper can satisfy it with a tiny shim, letting tests
// share a single cluster across the whole package via TestMain when the
// alternative (one cluster per test) would race against protoactor's
// process-global grpclog state.
type TB interface {
	Helper()
	Cleanup(func())
	Fatalf(format string, args ...any)
}

// defaultRequestTimeout is the per-cluster-call timeout for tests that
// use the simple Start constructor. Tests that exercise multiple distinct
// grain identities in a single run may need a longer timeout because each
// fresh identity pays an activation cost; those tests should call
// StartWithTimeout instead.
const defaultRequestTimeout = 2 * time.Second

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
	return StartWithTimeout(tb, defaultRequestTimeout, kinds...)
}

// StartWithTimeout is the Start variant that lets callers raise the
// per-cluster-call request timeout above the default. Useful for tests
// that exercise multiple fresh grain identities and therefore pay the
// activation cost on every distinct call.
func StartWithTimeout(tb TB, requestTimeout time.Duration, kinds ...*cluster.Kind) *cluster.Cluster {
	tb.Helper()

	autoPort, err := freeTCPPort()
	if err != nil {
		tb.Fatalf("failed to find free port for automanaged: %v", err)
	}

	system := actor.NewActorSystem()
	remoteCfg := remote.Configure("127.0.0.1", 0)
	provider := automanaged.NewWithConfig(
		automanagedRefreshTTL,
		autoPort,
		net.JoinHostPort("127.0.0.1", strconv.Itoa(autoPort)),
	)
	lookup := disthash.New()

	// cluster.Config.RequestLog must remain false. The cluster's built-in
	// RequestLog formatter logs whole proto request bodies via slog.Any,
	// which would leak message text, bearer tokens, and other payload
	// fields into the log stream. Protoactor defaults RequestLog to false,
	// so the invariant is preserved by inaction here — but it is the same
	// invariant the production wiring in internal/clusterboot pins, and
	// integration tests that assert the no-payload contract rely on it.
	clusterCfg := cluster.Configure(
		"blabby-test",
		provider,
		lookup,
		remoteCfg,
		cluster.WithKinds(kinds...),
		cluster.WithRequestTimeout(requestTimeout),
	)

	c := cluster.New(system, clusterCfg)
	c.StartMember()

	tb.Cleanup(func() { c.Shutdown(true) })

	// StartMember only sleeps 1s before returning; the automanaged
	// provider's HTTP listener and first gossip cycle may not have
	// completed. Block until the local member is reliably routable,
	// otherwise the first grain RPC races the cluster bootstrap and
	// fails with "max retries: 3" on slow CI runners.
	//
	// We can't poll cluster.MemberList directly: its Length method
	// reads the underlying member set without locking, racing the
	// automanaged provider's writer goroutine (upstream issue). Use
	// a race-safe HTTP probe of the provider's /_health endpoint
	// instead, then wait one refresh cycle for monitorStatuses to
	// publish the member into MemberList.
	waitForClusterReady(tb, autoPort)

	return c
}

// waitForClusterReady blocks until the automanaged provider's /_health
// endpoint on autoPort is reachable, then sleeps one gossip refresh
// interval (plus a small jitter buffer) so the next monitorStatuses
// cycle has registered the local member in MemberList.
func waitForClusterReady(tb TB, autoPort int) {
	tb.Helper()

	url := fmt.Sprintf("http://127.0.0.1:%d/_health", autoPort)
	client := &http.Client{Timeout: healthReadyPoll}

	deadline := time.Now().Add(healthReadyTimeout)
	for {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			tb.Fatalf("automanaged /_health on port %d did not become ready within %s", autoPort, healthReadyTimeout)
			return
		}
		time.Sleep(healthReadyPoll)
	}

	time.Sleep(automanagedRefreshTTL + gossipSettleBuffer)
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

// Package clustertest provides a thin bootstrap helper for in-process
// proto.actor cluster integration tests.
//
// The Start function is for in-process integration tests only; do not use it
// in production wiring. Production cluster startup lives in internal/clusterboot.
//
// # One cluster per package, shared across tests
//
// protoactor-go's remote.Start calls grpclog.SetLoggerV2, a process-global
// write. Booting a second cluster while a previous cluster's grpc balancer
// goroutine is still reading that global races under the race detector. The
// race lives in protoactor/grpc, not in blabby. A test package that needs a
// cluster should therefore start ONE in TestMain and share it across its
// tests — each test using a distinct grain identity so state never overlaps —
// rather than calling Start in every test. internal/grain/user/main_test.go is
// the canonical TestMain shape (a minimal testing.TB stand-in plus LIFO
// cleanups that run even if a test panics).
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
// automanaged.NewWithConfig. The readiness probe in waitForClusterReady
// converges as soon as a monitorStatuses cycle has registered the local
// member in MemberList, typically within one TTL of /_health binding.
const automanagedRefreshTTL = 2 * time.Second

// healthReadyTimeout bounds how long Start waits for the cluster to become
// routable: the automanaged provider's /_health endpoint answering, then the
// readiness-probe activation succeeding. On slow CI runners the provider's
// listener goroutine may not be scheduled for several hundred ms after
// StartMember returns, and the first gossip cycle adds automanagedRefreshTTL
// on top; 15s leaves comfortable headroom while still failing fast on a
// genuinely broken bootstrap.
const healthReadyTimeout = 15 * time.Second

// healthReadyPoll is the interval between readiness probes while waiting.
const healthReadyPoll = 50 * time.Millisecond

// readinessProbeKind is the no-op grain kind Start registers alongside the
// caller's kinds. Cluster readiness is probed by activating one instance of
// it through the real identity-lookup path; the actor itself does nothing,
// so the probe cannot disturb the kinds under test.
const readinessProbeKind = "clustertest-readiness-probe"

// TB is the subset of testing.TB used by Start. *testing.T satisfies it,
// and a TestMain helper can satisfy it with a tiny shim, letting tests
// share a single cluster across the whole package via TestMain when the
// alternative (one cluster per test) would race against protoactor's
// process-global grpclog state.
type TB interface {
	Helper()
	Cleanup(func())
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
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
	allKinds := make([]*cluster.Kind, 0, len(kinds)+1)
	allKinds = append(allKinds, kinds...)
	allKinds = append(allKinds, cluster.NewKind(readinessProbeKind, actor.PropsFromFunc(func(actor.Context) {})))

	clusterCfg := cluster.Configure(
		"blabby-test",
		provider,
		lookup,
		remoteCfg,
		cluster.WithKinds(allKinds...),
		cluster.WithRequestTimeout(requestTimeout),
	)

	c := cluster.New(system, clusterCfg)
	c.StartMember()

	tb.Cleanup(func() { shutdownCluster(tb, c) })

	// StartMember only sleeps 1s before returning; the automanaged
	// provider's HTTP listener and first gossip cycle may not have
	// completed, and a grain RPC issued before the topology lands fails
	// with "max retries: 3" (the cluster context's built-in retries back
	// off for nanoseconds, so they absorb nothing). Block until a probe
	// activation proves the member is routable.
	waitForClusterReady(tb, c, autoPort)

	return c
}

// waitForClusterReady blocks until the cluster can route a grain activation.
// Two gates run under one healthReadyTimeout deadline:
//
//  1. The automanaged provider's /_health endpoint answers 200 OK, so the
//     provider's discovery listener is up. Failing here points at a broken
//     bootstrap rather than slow gossip.
//  2. An activation of the readiness-probe kind through the real routing
//     path succeeds. cluster.Get resolves the owner via the disthash
//     rendezvous — read under the manager's own lock, race-safe unlike
//     MemberList's unlocked reads (upstream issue) — and round-trips an
//     ActivationRequest to the placement actor, returning nil until the
//     first gossip cycle has published the local member. The first non-nil
//     PID therefore proves exactly what tests need: the next grain RPC
//     routes instead of dying on "max retries".
//
// Gate 2 replaces a fixed automanagedRefreshTTL+buffer sleep. Polling the
// activation path converges as soon as the topology actually lands instead
// of always paying the worst case, and it cannot pass early.
func waitForClusterReady(tb TB, c *cluster.Cluster, autoPort int) {
	tb.Helper()

	deadline := time.Now().Add(healthReadyTimeout)

	url := fmt.Sprintf("http://127.0.0.1:%d/_health", autoPort)
	client := &http.Client{Timeout: healthReadyPoll}
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

	for {
		if pid := c.Get("readiness", readinessProbeKind); pid != nil {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("cluster did not become routable within %s: readiness-probe activation kept returning nil", healthReadyTimeout)
			return
		}
		time.Sleep(healthReadyPoll)
	}
}

// clusterShutdownTimeout bounds how long a test's cleanup waits for
// cluster.Shutdown to return. cluster.Shutdown(true) blocks until every actor
// has stopped, so a grain whose Terminate hangs would otherwise wedge the whole
// test binary until `go test` times out. Bounding it converts that into a
// clear, localized cleanup failure on the owning test.
const clusterShutdownTimeout = 10 * time.Second

// shutdownCluster shuts c down without ever blocking the test process longer
// than clusterShutdownTimeout. Shutdown runs in its own goroutine because it
// blocks until all actors stop and a buggy Terminate can block forever.
func shutdownCluster(tb TB, c *cluster.Cluster) {
	tb.Helper()
	done := make(chan struct{})
	go func() {
		c.Shutdown(true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(clusterShutdownTimeout):
		tb.Errorf("cluster shutdown did not complete within %s (a grain Terminate may be blocked)", clusterShutdownTimeout)
	}
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

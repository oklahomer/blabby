package clusterboot

import (
	"log/slog"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/cluster/clusterproviders/automanaged"
	"github.com/asynkron/protoactor-go/cluster/identitylookup/disthash"
	"github.com/asynkron/protoactor-go/remote"
	"go.opentelemetry.io/otel/metric"

	"github.com/oklahomer/blabby/internal/grain/maintenance"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/logging"
)

const (
	clusterName = "blabby"

	// automanagedRefreshTTL is the automanaged provider's gossip cycle: how
	// often it re-polls every seed's /_health endpoint and republishes the
	// active membership. It mirrors the in-process test bootstrap's value.
	automanagedRefreshTTL = 2 * time.Second
)

// GrainDeps bundles the collaborators the hosted grains need. Grouped into a
// struct (not positional args) so call sites read by field name as the set
// grows. RoomLoader is required; the persistence-backed fields may be nil, in
// which case the grains keep membership and messages in memory only (the
// behavior tests rely on) — production wires them all.
type GrainDeps struct {
	// Directory seeds each User grain activation's display name.
	Directory user.Directory
	// RoomLoader hydrates each Room grain activation's RoomRef.
	RoomLoader room.RoomLoader
	// Membership is the Room grain's DB-authoritative membership store.
	Membership room.MembershipStore
	// Messages is the Room grain's durable message store.
	Messages room.MessageStore
	// JoinedRooms hydrates each User grain's joined-rooms cache on activation.
	JoinedRooms user.JoinedRoomLoader
	// Sweeper runs the pending-account GC for the singleton maintenance grain.
	Sweeper maintenance.Sweeper
}

// Kinds returns the grain kinds blabby hosts: the User grain (which seeds each
// activation's display name and hydrates its joined rooms), the Room grain (which
// hydrates its RoomRef and member set), and the singleton maintenance grain (which
// runs periodic system jobs such as the pending-account sweep). Separated from
// Build so a test can assert the registered kinds without standing up an actor
// system.
func Kinds(deps GrainDeps) []*cluster.Kind {
	return []*cluster.Kind{
		user.NewKind(deps.Directory, user.WithJoinedRooms(deps.JoinedRooms)),
		room.NewKind(deps.RoomLoader, room.WithMembership(deps.Membership), room.WithMessages(deps.Messages)),
		maintenance.NewKind(deps.Sweeper),
	}
}

// Telemetry carries the optional observability collaborators for Build. The
// zero value leaves metrics disabled.
type Telemetry struct {
	// MeterProvider, when non-nil, enables proto.actor's built-in OpenTelemetry
	// instrumentation against this provider. A caller builds it via
	// internal/telemetry and exposes the paired scrape handler (see ADR-022).
	MeterProvider metric.MeterProvider
}

// Build constructs (but does not start) a proto.actor cluster from cc, hosting
// the given grain kinds. The caller starts the returned cluster with
// StartMember.
//
// Single-node (no seeds): the remote transport binds an OS-assigned ephemeral
// port on loopback and automanaged.New runs self-discovery against its own
// defaults. A lone node accepts no inbound peer connections, so neither the
// bind port nor an advertised host is load-bearing.
//
// Multi-node (one or more seeds): the bind port and advertised host become
// load-bearing. Peers reach this node at cc.advertisedHost, which the remote
// layer writes to ProcessRegistry.Address; that address is what every
// cross-node grain message and PID-in-payload fan-out resolves to (ADR-011), so
// it must be a real peer-reachable host:port on a fixed port. automanaged is a
// static-list provider: cc.seeds is the full set of every member's
// host:discoveryPort endpoint, polled each refresh cycle.
//
// cluster.Config.RequestLog is intentionally left at its false default: the
// built-in RequestLog formatter logs whole proto request bodies via slog.Any,
// which would leak message text and bearer tokens into the log stream.
//
// The actor system is built from a single actor.Config path so proto.actor's
// own logging always joins blabby's JSON stream at Warn and above via
// protoActorLogger. When tel.MeterProvider is set, that same config enables
// proto.actor's OpenTelemetry metrics — which requires BOTH the provider and
// the MetricsEnabled flag; a provider alone yields a no-op extension and an
// empty scrape (ADR-022). The zero-value Telemetry leaves both unset.
func Build(cc Config, tel Telemetry, kinds ...*cluster.Kind) *cluster.Cluster {
	sysCfg := actor.NewConfig()
	sysCfg.LoggerFactory = protoActorLogger
	if tel.MeterProvider != nil {
		sysCfg.MetricsProvider = tel.MeterProvider
		sysCfg.MetricsEnabled = true
	}
	system := actor.NewActorSystemWithConfig(sysCfg)

	// Honor an explicitly supplied advertised host whenever one is set, not only
	// in multi-node mode: a single-node config that opts into a fixed advertised
	// address still gets it. Multi-node always sets it (validation requires it);
	// single-node usually leaves it empty and lets the listener auto-derive.
	var remoteOpts []remote.ConfigOption
	if cc.advertisedHost != "" {
		remoteOpts = append(remoteOpts, remote.WithAdvertisedHost(cc.advertisedHost))
	}
	remoteCfg := remote.Configure(cc.bindHost, cc.bindPort, remoteOpts...)

	var provider cluster.ClusterProvider
	if cc.MultiNode() {
		provider = automanaged.NewWithConfig(automanagedRefreshTTL, cc.discoveryPort, cc.seeds...)
	} else {
		provider = automanaged.New()
	}
	lookup := disthash.New()

	cfg := cluster.Configure(
		clusterName,
		provider,
		lookup,
		remoteCfg,
		cluster.WithKinds(kinds...),
	)
	return cluster.New(system, cfg)
}

// protoActorLogger routes proto.actor's internal logger through the process JSON
// slog stream at Warn and above. It captures slog.Default()'s handler at call
// time — each binary calls logging.SetupDefault() before Build, so the JSON
// handler is already installed — and raises the floor to Warn with
// logging.NewMinLevelHandler, tagging every line with the library and system id.
//
// Info is dropped deliberately. proto.actor's built-in dead-letter subscriber
// logs whole message bodies at Info — the same leak class the RequestLog default
// above avoids — and its cluster startup chatter duplicates blabby's own
// server.cluster.* lines, so no operator signal is lost.
func protoActorLogger(system *actor.ActorSystem) *slog.Logger {
	return slog.New(logging.NewMinLevelHandler(slog.Default().Handler(), slog.LevelWarn)).
		With("lib", "proto.actor").
		With("system", system.ID)
}

// ShutdownClient stops a cluster the local node joined with StartClient. It is
// the client-safe counterpart to cluster.Cluster.Shutdown, which assumes a
// member: that method's first step is Gossip.SetState, and the gossiper is
// started only by StartMember, so it nil-panics for a client. ShutdownClient
// stops what a client actually runs — the actor system, which owns the
// UserConnection actors and the remote endpoint manager, and the remote
// transport.
func ShutdownClient(c *cluster.Cluster) {
	c.ActorSystem.Shutdown()
	c.Remote.Shutdown(true)
}

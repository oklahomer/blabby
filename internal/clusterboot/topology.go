package clusterboot

import (
	"log/slog"

	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/eventstream"
)

// SubscribeTopologyLogging subscribes cluster-membership logging to c's
// EventStream and returns the subscription. Subscribe before StartMember: the
// first topology — the local member, and any peers already reachable — is
// published while the member starts, so a later subscription would miss those
// joins (it would still see subsequent leaves, but never the matching join).
// Unsubscribe on shutdown, or let it die with the actor system.
func SubscribeTopologyLogging(c *cluster.Cluster) *eventstream.Subscription {
	return c.ActorSystem.EventStream.Subscribe(topologyLogHandler)
}

// topologyLogHandler is the EventStream subscription handler that logs cluster
// membership changes. The actor system publishes many event types on the shared
// stream, so it forwards only *cluster.ClusterTopology to logTopologyChange and
// ignores the rest.
func topologyLogHandler(evt any) {
	if topology, ok := evt.(*cluster.ClusterTopology); ok {
		logTopologyChange(topology)
	}
}

// logTopologyChange emits one structured log line per cluster member that
// joined or left in topology. It is side-effect-free apart from the slog calls,
// so a test can drive it directly. Only non-sensitive membership fields are
// logged — the node host:port and its kinds — never request payloads or
// credentials.
func logTopologyChange(topology *cluster.ClusterTopology) {
	for _, m := range topology.Joined {
		slog.Info("server.cluster.member_joined", "node_address", m.Address(), "kinds", m.Kinds)
	}
	for _, m := range topology.Left {
		slog.Info("server.cluster.member_left", "node_address", m.Address(), "kinds", m.Kinds)
	}
}

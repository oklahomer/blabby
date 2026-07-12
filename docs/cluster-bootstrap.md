# Cluster bootstrap: members, clients, and discovery

One function assembles the actor system, the gRPC transport, the discovery
provider, and the identity lookup for both binaries:
`Build` in `internal/clusterboot/bootstrap.go`. This tour walks that
assembly and points out which pieces become load-bearing the moment a second
node joins. The runnable companion is
[`multi-node-cluster.md`](multi-node-cluster.md).

## Member vs client

A Proto.Actor cluster distinguishes nodes that *host* grains from nodes that
only *call* them. The backend joins with `StartMember` and registers the
three grain kinds; the gateway joins with `StartClient`, registers no kinds,
and routes every grain call to whichever member owns the identity. That is
the tier split in one sentence
([ADR-016](adr/adr-016-gateway-backend-tier-separation.md)): the API tier
scales on connection count, the grain tier on state and traffic, and neither
drags the other along. Official page:
[cluster](https://proto.actor/docs/ProtoActor/cluster).

Shutdown is asymmetric too, and the code carries a lesson learned:
`Cluster.Shutdown` assumes a member, because its first act writes gossip
state, and a client never starts the gossiper. `ShutdownClient`
(`bootstrap.go`) stops what a client actually runs, the actor system and the
remote transport, and its comment explains why.

## Discovery: the automanaged provider

Members find each other through a cluster provider. blabby uses
`automanaged`: every node gets the full static list of seed endpoints and
polls each seed's health endpoint every refresh cycle (two seconds here).
No Consul, no etcd, no Kubernetes; the entire discovery story fits a
laptop. The provider zoo that ships with protoactor-go (k8s, Consul, etcd,
Zookeeper) is the production path, and swapping one in touches only this
one construction site:

```go
var provider cluster.ClusterProvider
if cc.MultiNode() {
    provider = automanaged.NewWithConfig(automanagedRefreshTTL, cc.discoveryPort, cc.seeds...)
} else {
    provider = automanaged.New()
}
lookup := disthash.New()
```

For the backend, zero configuration means single-node mode: the transport
binds an ephemeral loopback port and self-discovery runs against its own
defaults. The gateway's flagless default is different by design (`Defaults`
in `internal/clusterboot/config.go`): it joins that local backend as a
cluster client, seeds and all, which is why the quick start needs no flags
on either binary. Multi-node mode needs three flags per node (`--seeds`, a
fixed `--cluster-port`, a peer-reachable `--advertised-host`), and
`Config.Warnings` flags the configurations that are legal but usually a
mistake, a loopback advertised host first among them.

## Identity lookup: disthash

The identity lookup decides which member owns which grain identity.
`disthash` hashes each identity across the current membership: every member
can compute the owner locally, and when the topology changes, ownership
rebalances and grains re-activate on their new owners at the next message.
The multi-member test
(`internal/clusterboot/departure_integration_test.go`) exercises exactly
that: kill a member, watch its grains come back on the survivor.

## Advertised host and PIDs in payloads

`remote.WithAdvertisedHost` feeds the address every other node uses to reach
this one, and it does double duty. Beyond ordinary grain routing, blabby
puts connection-actor PIDs *inside message payloads*
(`RegisterConnection`), and a PID is only as good as the address baked into
it. The advertised host is what makes a PID minted on the gateway resolvable
from any backend ([ADR-011](adr/adr-011-cross-boundary-pid-propagation.md)).
Everything on the wire is protobuf, both blabby's own contracts under
`proto/` and Proto.Actor's internals. Official pages:
[remote](https://proto.actor/docs/ProtoActor/remote),
[serialization](https://proto.actor/docs/ProtoActor/serialization).

## What the config deliberately leaves off

`cluster.Config.RequestLog` stays at its false default: the built-in
request logger serializes whole request bodies into the log stream, which
would leak message text and bearer tokens. The observability tour covers
how blabby logs grain traffic without paying that price.

## Try it

- Start a single backend (`go run ./cmd/backend`) and read the boot
  sequence: `server.cluster.member_joined` lists the node's address and its
  three grain kinds, then `server.cluster.started` confirms the advertised
  address peers would use.
- Follow [`multi-node-cluster.md`](multi-node-cluster.md) to run two
  backends and a gateway, join a room, then kill the backend that logged the
  room's `grain.activated`. The survivors log
  `server.cluster.member_left`, and the next message to that room
  re-activates its grain on the remaining member: same identity, new
  address, no client involvement.

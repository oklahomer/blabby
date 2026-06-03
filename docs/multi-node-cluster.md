# Running a multi-node cluster

By default the server runs as a single self-contained node (`go run ./cmd/server`,
see the [Quick Start](../README.md#quick-start)). Supplying one or more discovery
**seeds** opts into multi-node mode: several server instances discover each other,
form one cluster, and spread the User and Room grains across the available nodes.
A message sent through a node that does not host the target Room grain is routed
transparently to whichever node does — no client-visible change.

## How discovery works

Nodes find each other through Proto.Actor's **automanaged** provider. It is a
*static-list* provider: each node is told the full set of peer discovery
endpoints up front and polls every one of them. There is no external registry
(no Consul, etcd, or Kubernetes API) — which is exactly what keeps a single
binary dependency-free, at the cost of having to list the peers explicitly.

Each node runs two listeners:

- a **remote transport** (`--cluster-host` / `--cluster-port`) that carries
  grain-to-grain traffic, and
- an **automanaged discovery** endpoint (`--discovery-port`) that exposes a
  health check the other nodes poll.

## Flags

| Flag | Meaning | Multi-node requirement |
|------|---------|------------------------|
| `--listen` | HTTP/WebSocket gateway address for clients | Unique per node on one host |
| `--cluster-host` | Remote transport bind host (default `127.0.0.1`) | Bind address; defaults to loopback |
| `--cluster-port` | Remote transport bind port | **Must be a fixed, non-zero port** — an ephemeral port cannot be advertised |
| `--advertised-host` | `host:port` peers use to reach this node | **Required**, must be peer-reachable, and its port should equal `--cluster-port` |
| `--discovery-port` | automanaged discovery (gossip) port (default `6330`) | Unique per node on one host |
| `--seeds` | Comma-separated `host:discoveryPort` list of **all** members, including this node | Required (its presence is what enables multi-node mode) |

The server refuses to start (non-zero exit) if multi-node mode is requested with
an empty `--advertised-host` or a `0`/ephemeral `--cluster-port`, and logs the
resolved advertised address at startup so you can confirm what peers will see. A
loopback advertised host (e.g. `127.0.0.1`) is accepted with a warning: it is
correct for the same-host demo below, but unreachable from another machine.

## The same JWT secret on every node — required

Authentication is stateless: a node validates a bearer token with its own
`--jwt-secret`. A token issued by one node is presented to another on cross-node
calls, so **every node must run with the identical signing secret.** If the
secrets differ, cross-node auth fails and the mismatch is not detectable at
startup — there is no error, messages simply do not flow.

The zero-config development secret is shared by construction, so the same-host
demo below works without `--jwt-secret`. Any real deployment **must** pass the
same explicit `--jwt-secret` to all nodes.

## Two nodes on one machine

Run each command in its own terminal. The two nodes share one `--seeds` list
naming both discovery endpoints; each advertises its own `host:port` on a port
that matches its `--cluster-port`.

**Node A:**

```bash
go run ./cmd/server \
  --listen 127.0.0.1:8080 \
  --cluster-host 127.0.0.1 \
  --cluster-port 8091 \
  --discovery-port 6330 \
  --advertised-host 127.0.0.1:8091 \
  --seeds 127.0.0.1:6330,127.0.0.1:6331
```

**Node B:**

```bash
go run ./cmd/server \
  --listen 127.0.0.1:8081 \
  --cluster-host 127.0.0.1 \
  --cluster-port 8092 \
  --discovery-port 6331 \
  --advertised-host 127.0.0.1:8092 \
  --seeds 127.0.0.1:6330,127.0.0.1:6331
```

> For a real deployment add the same `--jwt-secret <value>` to both commands and
> set each `--advertised-host` to a routable address (a LAN IP, pod IP, or
> resolvable hostname) rather than `127.0.0.1`.

### Confirm the cluster formed

Once both nodes are up, each logs the other's arrival. On **Node A** you will
see a line naming Node B's advertised address:

```json
{"level":"INFO","msg":"server.cluster.member_joined","node_address":"127.0.0.1:8092","kinds":["UserGrain","RoomGrain"]}
```

and symmetrically on **Node B** for `127.0.0.1:8091`. A node leaving logs
`server.cluster.member_left` with the same fields. (Because both nodes advertise
loopback, each also logs a one-time `server.cluster.config_warning` — expected
for this same-host demo.)

### Drive a message across nodes

Open two clients, each pointed at a different node:

```bash
# Terminal 3 — talks to Node A
go run ./cmd/client --server http://localhost:8080

# Terminal 4 — talks to Node B
go run ./cmd/client --server http://localhost:8081
```

Sign in as different users (for example `alice` on the first client and `bob` on
the second — credentials are in the [Quick Start](../README.md#quick-start)).
Press `/` in each client, join the **general** room, make it active, and send a
message from one client. It appears in the other. Because the two clients are
connected to different nodes, that round-trip exercises cross-node routing: the
sender's User grain, the Room grain, and the recipient's connection may each live
on a different node, and Proto.Actor routes between them transparently.

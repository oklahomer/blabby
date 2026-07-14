# Running multiple gateways and backends

The [Quick Start](../README.md#quick-start) runs the minimal pair: one backend
(`cmd/backend`, a cluster **member** hosting the grains) and one gateway
(`cmd/gateway`, a cluster **client** serving HTTP/WebSocket). Because the two are
separate binaries, you scale them independently — several gateways fronting one
or more backends. A message sent through any gateway is routed transparently to
whichever backend hosts the target Room grain, and fanned back out to the
recipients' connections on whichever gateways hold them.

## How it works

Both tiers join one Proto.Actor cluster through the **automanaged** provider, a
*static-list* discovery mechanism: each process is told the discovery endpoints
to poll, with no external registry (no Consul, etcd, or Kubernetes API). That is
what keeps the binaries dependency-free, at the cost of listing the backends
explicitly.

- A **backend** is a cluster *member*: it hosts grains and is a placement target.
- A **gateway** is a cluster *client*: it calls grains and hosts the
  per-connection `UserConnection` actors, but never hosts grains. It does not
  appear in the cluster's member topology.

Each process runs a **remote transport** (`--cluster-host` / `--cluster-port`)
for grain traffic and an **automanaged discovery** endpoint (`--discovery-port`)
that peers poll. A gateway seeds the backends' discovery endpoints so it can find
them; backends reach a gateway's `UserConnection` actors at the gateway's
**advertised host** for fan-out — so the advertised host must be correct on both
tiers.

## Flags

| Flag | Tier | Meaning |
|------|------|---------|
| `--listen` | gateway | HTTP/WebSocket address for clients (unique per gateway on one host) |
| `--jwt-secret` | gateway | JWT signing secret (see below); backends never see tokens |
| `--cluster-host` | both | Remote transport bind host (default `127.0.0.1`) |
| `--cluster-port` | both | Remote transport bind port — **fixed, non-zero** when seeds are supplied |
| `--advertised-host` | both | `host:port` peers use to reach this process; **required** with seeds, port should equal `--cluster-port` |
| `--discovery-port` | both | automanaged discovery port (default `6330` for the backend, `6331` for the gateway); unique per process on one host |
| `--seeds` | both | Comma-separated `host:discoveryPort` list of the **backends** to discover |

Supplying one or more `--seeds` selects multi-node mode. In that mode a process
refuses to start (non-zero exit) on an empty `--advertised-host` or a
`0`/ephemeral `--cluster-port`, and logs the resolved advertised address at
startup. A loopback advertised host (e.g. `127.0.0.1`) is accepted with a
warning — correct for the same-host demo below, unreachable from another machine.

## The same JWT secret on every gateway — required

Authentication happens entirely at the gateway tier: a gateway issues a token at
`/login` and validates it on every request and on the WebSocket. The backend
never sees a JWT. So the rule is **every gateway must run with the identical
`--jwt-secret`** — a token issued by one gateway is presented to another when a
client reconnects or load-balances. If gateway secrets differ, auth fails on the
other gateway; the mismatch is silent at startup.

The zero-config development secret is shared by construction, so the same-host
demo below works without `--jwt-secret`. Any real deployment **must** pass the
same explicit `--jwt-secret` to all gateways.

## One backend, two gateways on one machine

A *single* local gateway needs no flags — `go run ./cmd/gateway` defaults to a
loopback backend on `127.0.0.1:6330` (see the [Quick Start](../README.md#quick-start)).
The explicit flags below are only needed to run **multiple** gateways on one
host, where each needs a distinct listen/cluster/discovery port.

Run each command in its own terminal. The gateways both seed the backend's
discovery endpoint (`127.0.0.1:6330`); each process advertises its own
`host:port` on a port matching its `--cluster-port`, with a discovery port unique
to this host.

**Backend** (member, remote `8090`, discovery `6330`):

```bash
go run ./cmd/backend \
  --cluster-host 127.0.0.1 \
  --cluster-port 8090 \
  --discovery-port 6330 \
  --advertised-host 127.0.0.1:8090 \
  --seeds 127.0.0.1:6330
```

**Gateway 1** (client, HTTP `:8080`, remote `8091`, discovery `6331`):

```bash
go run ./cmd/gateway \
  --listen 127.0.0.1:8080 \
  --cluster-port 8091 \
  --discovery-port 6331 \
  --advertised-host 127.0.0.1:8091 \
  --seeds 127.0.0.1:6330
```

**Gateway 2** (client, HTTP `:8081`, remote `8092`, discovery `6332`):

```bash
go run ./cmd/gateway \
  --listen 127.0.0.1:8081 \
  --cluster-port 8092 \
  --discovery-port 6332 \
  --advertised-host 127.0.0.1:8092 \
  --seeds 127.0.0.1:6330
```

> For a real deployment add the same `--jwt-secret <value>` to both gateways and
> set every `--advertised-host` to a routable address (a LAN IP, pod IP, or
> resolvable hostname) rather than `127.0.0.1`. To scale the grain tier too, run
> several backends — give each a fixed `--cluster-port`/`--advertised-host` and a
> shared `--seeds` listing every backend's discovery endpoint, and point the
> gateways' `--seeds` at all of them.

### Confirm the cluster formed

Each **gateway** logs the backend's arrival, naming its advertised address:

```json
{"level":"INFO","msg":"server.cluster.member_joined","node_address":"127.0.0.1:8090","kinds":["prototopic","UserGrain","RoomGrain","MaintenanceGrain"]}
```

`prototopic` is Proto.Actor's built-in pub-sub kind; the other three are
blabby's grains.

The gateways are clients, not members, so they never appear in the cluster's
member topology — only backends do. A member leaving logs
`server.cluster.member_left` with the same fields. (Because every process here
advertises loopback, each also logs a one-time `server.cluster.config_warning` —
expected for this same-host demo.)

### Drive a message across gateways

Open two clients, each pointed at a different gateway:

```bash
# Terminal — talks to Gateway 1
go run ./cmd/client --server http://localhost:8080

# Terminal — talks to Gateway 2
go run ./cmd/client --server http://localhost:8081
```

Sign in as different users (for example `alice` on the first client and `bob` on
the second — credentials are in the [Quick Start](../README.md#quick-start)).
Press `/` in each client, join the **general** room, make it active, and send a
message from one client. It appears in the other. The two clients are connected
to different gateways, so the round-trip crosses tiers and gateways: the sender's
User grain and the Room grain live on the backend, and the backend fans the
message out to the recipient's `UserConnection` on the *other* gateway — all
routed by Proto.Actor without either client knowing where anything lives.

# ADR-016: Gateway as cluster client, backend as cluster member — tier separation

- **Status:** Accepted
- **Date:** 2026-06-03
- **Related:** [ADR-001](adr-001-grain-topology.md), [ADR-007](adr-007-database-authoritative-persistence.md), [ADR-011](adr-011-cross-boundary-pid-propagation.md)

## Context

blabby's client-facing API and its grain tier have different scaling
characteristics. The API tier terminates HTTP and WebSocket connections and
holds one `UserConnection` actor per live socket — it scales with connection
count and edge throughput. The grain tier holds `User` and `Room` grain state and
routes between grains — it scales with the grain population and message volume.
Hosting both in one process forces them to scale together, which a chat workload
does not want.

Proto.Actor supports two ways to join a cluster:

- A **member** (`StartMember()`) hosts grain activations and is a placement
  target.
- A **client** (`StartClient()`) starts the remote transport — so it is
  addressable and grains can send messages to actors on it — and calls grains
  without hosting any.

Two design choices make a client-hosted edge workable:

- [ADR-001](adr-001-grain-topology.md) models the `UserConnection` as a regular
  actor, not a grain. A regular actor is spawned explicitly on the node that owns
  its socket, so it can live on a node that hosts no grains.
- [ADR-011](adr-011-cross-boundary-pid-propagation.md) carries the caller's
  `*actor.PID` in the proto request body, so a grain reaches a `UserConnection`
  by the address that PID names — across a process boundary — without knowing
  where the connection lives.

## Decision

**blabby runs as two binaries, one per cluster role.**

- **`cmd/backend`** joins with `StartMember()` and hosts the `User` and `Room`
  grains. It serves no HTTP.
- **`cmd/gateway`** joins with `StartClient()`, serves the HTTP and WebSocket
  endpoints, and hosts the per-connection `UserConnection` actors. It calls
  grains but hosts none, so it is never a placement target.

Supporting choices:

- The shared cluster wiring (flag parsing and validation, cluster construction,
  membership logging) lives in one internal package, so each `main` is a thin
  composition root that differs only in `StartMember` vs `StartClient` and in
  whether it builds an HTTP stack.
- Each tier depends only on the slice of the user store it needs: the gateway
  uses it for credential lookup (login and token issue); the backend uses it as
  the `User` grain's display-name directory.
- JWT handling belongs to the gateway alone. The gateway issues a token at login
  and validates it on every request and on the WebSocket; the backend never sees
  a token. Behind the gateway, identity travels as a parsed user ID.

Cross-tier message delivery uses the PID-in-payload mechanism from ADR-011: the
backend Room grain fans a message out to each member's `UserConnection`, which
may sit on a different node than the grain.

## Consequences

### Positive

- **Independent scaling.** The edge tier (WebSocket capacity, per-connection
  memory, fan-out writes) and the grain tier (grain state, routing) scale on
  separate axes.
- **Gateways are never placement targets.** Adding gateways adds API capacity
  without spreading grain state onto edge nodes.
- **The user store's two responsibilities are cleanly separated** — credential
  lookup on the gateway, identity resolution on the backend — each satisfied by
  the interface its tier actually uses.
- **The JWT secret has a smaller blast radius:** only gateways sign and verify
  tokens, so the shared-secret requirement applies to gateways alone.
- **Each binary reads as a single role**, which suits a codebase meant to be
  read.

### Negative

- **Two deployable units** to build, configure, and operate, and the proto
  contract becomes a contract between independently deployed tiers, so version
  skew must be managed on rollout.
- **A gateway needs a peer-reachable advertised address**, because backends reach
  its `UserConnection` PIDs for fan-out. A misconfigured advertised host produces
  the same silent dead-letter failure ADR-011 describes for members, now on the
  edge as well.
- **A gateway still participates in discovery.** A client polls the backends'
  discovery endpoints and tracks topology to route grain calls; it is not a
  dependency-free HTTP process.

### Neutral

- **Both tiers share one authoritative user store.** Accounts live in the
  PostgreSQL `service_user` table ([ADR-007](adr-007-database-authoritative-persistence.md)),
  which each tier reaches through the tier-specific seam it needs — the gateway
  for credential lookup, registration, and verification; the backend for the
  `User` grain's directory and the Room grain's membership resolution. Tier
  separation does not fragment user data or hand either tier its own copy: the
  store is DB-authoritative and mutable, read and written through those seams.
- **A single binary with a `--role` flag** is an alternative: it keeps one entry
  point but reintroduces role-branching that two binaries avoid.
- **A fully cluster-agnostic edge** — gateways speaking only HTTP/gRPC to the
  cluster with no remote transport, routing events back through a pub/sub or
  streaming layer — would let gateways sit behind an ordinary load balancer, but
  it discards the PID-based fan-out this design relies on and adds a
  routing/affinity layer.

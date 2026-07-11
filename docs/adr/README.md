# Architecture Decision Records

This directory contains Architecture Decision Records (ADRs) for the blabby project. ADRs capture the key architectural decisions made during development, including the context, reasoning, and consequences of each decision.

Each ADR follows a standard format: title, Status/Date/Related metadata, Context, Decision, Consequences (positive/negative/neutral), alternatives considered, and references.

ADR numbers are permanent identifiers assigned in creation order — they are not a reading order. The **Reading order** below groups the records by theme so the design can be read as a coherent whole; the **Index** that follows lists every ADR by number.

## Reading order

### Foundations — the actor topology and how processes are organized

| ADR | Why read it |
|-----|-------------|
| [ADR-001](adr-001-grain-topology.md) | Why User and Room are grains and UserConnection is a regular actor — the shape everything else builds on. |
| [ADR-016](adr-016-gateway-backend-tier-separation.md) | Why the gateway is a cluster client and the backend a cluster member. |
| [ADR-017](adr-017-supervision-strategy.md) | Which actors Stop and which Restart when they fail. |

### Protocol & authentication — how clients talk to the system

| ADR | Why read it |
|-----|-------------|
| [ADR-002](adr-002-client-protocol.md) | Why HTTP for commands/queries and WebSocket for events, both JSON. |
| [ADR-003](adr-003-websocket-authentication.md) | Why the WebSocket authenticates with a first message, not a query-parameter token. |
| [ADR-018](adr-018-http-authentication-boundary.md) | Why auth is gateway middleware with transport-agnostic primitives and explicit identity arguments. |

### Messaging & consistency — how state moves and converges

| ADR | Why read it |
|-----|-------------|
| [ADR-004](adr-004-message-routing-through-user-grain.md) | Why the User grain acts as the user's agent for routing. |
| [ADR-005](adr-005-unconditional-fan-out.md) | Why the Room grain sends to all members regardless of connection state. |
| [ADR-015](adr-015-command-query-vs-notification.md) | Synchronous request/response vs. asynchronous best-effort notification (Room fan-out as the worked example). |
| [ADR-010](adr-010-eventual-consistency-model.md) | Why grains are single-writer and reads are eventually consistent between grains. |
| [ADR-011](adr-011-cross-boundary-pid-propagation.md) | Why a `*actor.PID` travels in the proto body, not `ctx.Sender()`. |
| [ADR-012](adr-012-watch-based-connection-lifecycle.md) | Why there is no explicit Deregister — connection lifecycle is keyed by PID via watch. |
| [ADR-013](adr-013-business-errors-as-response-values.md) | Why business errors are a shared `ErrorDetail` response value, not a gRPC status. |
| [ADR-009](adr-009-error-response-format.md) | Why errors carry dual numeric + string codes with range encoding. |
| [ADR-006](adr-006-bidirectional-watch-pattern.md) | *(Proposed)* the planned bidirectional-watch self-healing for reconnect. |

### Persistence & data — the store, its keys, and search

| ADR | Why read it |
|-----|-------------|
| [ADR-007](adr-007-database-authoritative-persistence.md) | Why PostgreSQL is authoritative: normalized entities plus an append-only event journal, grains hydrating from the store. |
| [ADR-008](adr-008-no-redis.md) | Why grain memory hydrated from the store replaces a cache tier — no Redis. |
| [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md) | Numeric Snowflake ids internally, opaque public codes externally, parsed once at boundaries. |
| [ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md) | How time-ordered ids are minted safely across a cluster via a worker lease. |
| [ADR-020](adr-020-pgroonga-search-stack.md) | Why CJK-capable full-text and substring search runs inside PostgreSQL via PGroonga. |

### Operations — periodic work

| ADR | Why read it |
|-----|-------------|
| [ADR-021](adr-021-scheduled-maintenance-jobs.md) | Why periodic jobs run via an internal trigger, a singleton grain, and an advisory-lock backstop. |
| [ADR-022](adr-022-protoactor-metrics-exposure.md) | Why Proto.Actor metrics run through an owned MeterProvider on a dedicated Prometheus registry, exposed per binary. |

## Index

| ADR | Title | Status | Date |
|-----|-------|--------|------|
| [ADR-001](adr-001-grain-topology.md) | Grain topology -- why User and Room are grains, UserConnection is a regular actor | Accepted | 2026-06-13 |
| [ADR-002](adr-002-client-protocol.md) | Client protocol -- why HTTP for commands and queries, WebSocket for events, both JSON | Accepted | 2026-06-13 |
| [ADR-003](adr-003-websocket-authentication.md) | WebSocket authentication -- why first-message auth over query parameter token | Accepted | 2026-06-13 |
| [ADR-004](adr-004-message-routing-through-user-grain.md) | Message routing through User grain -- why User grain acts as the user's agent | Accepted | 2026-06-13 |
| [ADR-005](adr-005-unconditional-fan-out.md) | Unconditional fan-out -- why Room grain sends to all members regardless of connection state | Accepted | 2026-06-13 |
| [ADR-006](adr-006-bidirectional-watch-pattern.md) | Bidirectional watch pattern -- why both sides watch each other for self-healing | Proposed | -- |
| [ADR-007](adr-007-database-authoritative-persistence.md) | Database-authoritative persistence -- normalized entities plus an append-only event journal | Accepted | 2026-07-05 |
| [ADR-008](adr-008-no-redis.md) | No Redis -- why grain memory hydrated from the store replaces a cache tier | Accepted | 2026-07-05 |
| [ADR-009](adr-009-error-response-format.md) | Error response format -- why dual numeric + string codes with range encoding | Accepted | 2026-06-13 |
| [ADR-010](adr-010-eventual-consistency-model.md) | Eventual consistency model -- why grains are single-writer and reads are eventually consistent | Accepted | 2026-06-13 |
| [ADR-011](adr-011-cross-boundary-pid-propagation.md) | Cross-boundary PID propagation -- pass `*actor.PID` in the proto body, not `ctx.Sender()` | Accepted | 2026-04-30 |
| [ADR-012](adr-012-watch-based-connection-lifecycle.md) | Watch-based connection lifecycle -- no explicit Deregister, key by PID | Accepted | 2026-05-02 |
| [ADR-013](adr-013-business-errors-as-response-values.md) | Business errors as response values -- carried by a shared `ErrorDetail`, not gRPC status | Accepted | 2026-05-03 |
| [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md) | Domain identifier types -- numeric Snowflake ids internally, opaque public codes externally, parsed at boundaries | Accepted | 2026-07-05 |
| [ADR-015](adr-015-command-query-vs-notification.md) | Command/query vs. notification -- synchronous request/response or asynchronous best-effort (Room fan-out as the worked example) | Accepted | 2026-05-31 |
| [ADR-016](adr-016-gateway-backend-tier-separation.md) | Gateway as cluster client, backend as cluster member -- tier separation | Accepted | 2026-06-03 |
| [ADR-017](adr-017-supervision-strategy.md) | Supervision policy for non-grain actors -- Stop when a rebuilt instance can't do useful work, Restart when it can | Accepted | 2026-06-08 |
| [ADR-018](adr-018-http-authentication-boundary.md) | HTTP authentication boundary -- middleware in the gateway, transport-agnostic primitives in auth, identity as explicit arguments | Accepted | 2026-06-13 |
| [ADR-019](adr-019-snowflake-ids-and-worker-lease-fencing.md) | Snowflake ids and worker-lease fencing -- time-ordered ids minted safely across a cluster | Accepted | 2026-07-05 |
| [ADR-020](adr-020-pgroonga-search-stack.md) | PGroonga search stack -- CJK-capable full-text in PostgreSQL, hand-written SQL over pgx | Accepted | 2026-07-05 |
| [ADR-021](adr-021-scheduled-maintenance-jobs.md) | Scheduled maintenance jobs -- an internal trigger, a singleton grain, and an advisory-lock backstop | Accepted | 2026-07-06 |
| [ADR-022](adr-022-protoactor-metrics-exposure.md) | Proto.Actor metrics exposure -- an owned MeterProvider on a dedicated Prometheus registry, exposed per binary | Accepted | 2026-07-11 |

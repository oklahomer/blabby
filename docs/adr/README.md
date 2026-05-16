# Architecture Decision Records

This directory contains Architecture Decision Records (ADRs) for the blabby project. ADRs capture the key architectural decisions made during development, including the context, reasoning, and consequences of each decision.

Each ADR follows a standard format: Title, Status, Context, Decision, Consequences.

## ADR Index

| ADR | Title | Status | Date |
|-----|-------|--------|------|
| [ADR-001](adr-001-grain-topology.md) | Grain topology -- why User and Room are grains, UserConnection is a regular actor | Proposed | -- |
| [ADR-002](adr-002-client-protocol.md) | Client protocol -- why HTTP POST for commands, WebSocket for events, both JSON | Proposed | -- |
| [ADR-003](adr-003-websocket-authentication.md) | WebSocket authentication -- why first-message auth over query parameter token | Proposed | -- |
| [ADR-004](adr-004-message-routing-through-user-grain.md) | Message routing through User grain -- why User grain acts as the user's agent | Proposed | -- |
| [ADR-005](adr-005-unconditional-fan-out.md) | Unconditional fan-out -- why Room grain sends to all members regardless of connection state | Proposed | -- |
| [ADR-006](adr-006-bidirectional-watch-pattern.md) | Bidirectional watch pattern -- why both sides watch each other for self-healing | Proposed | -- |
| [ADR-007](adr-007-single-table-persistence.md) | Single-table persistence -- why normalized columns + JSONB in one table | Proposed | -- |
| [ADR-008](adr-008-no-redis.md) | No Redis -- why grain in-memory state replaces a cache layer | Proposed | -- |
| [ADR-009](adr-009-error-response-format.md) | Error response format -- why dual numeric + string codes with range encoding | Proposed | -- |
| [ADR-010](adr-010-eventual-consistency-model.md) | Eventual consistency model -- why grains are single-writer and reads are eventually consistent | Proposed | -- |
| [ADR-011](adr-011-cross-boundary-pid-propagation.md) | Cross-boundary PID propagation -- pass `*actor.PID` in the proto body, not `ctx.Sender()` | Accepted | 2026-04-30 |
| [ADR-012](adr-012-watch-based-connection-lifecycle.md) | Watch-based connection lifecycle -- no explicit Deregister, key by PID | Accepted | 2026-05-02 |
| [ADR-013](adr-013-business-errors-as-response-values.md) | Business errors as response values -- carried by a shared `ErrorDetail`, not gRPC status | Accepted | 2026-05-03 |
| [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md) | Domain identifier types -- `UserID` / `RoomID` value objects parsed once at four boundaries | Accepted | 2026-05-16 |

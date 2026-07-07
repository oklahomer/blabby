// Package persistence provides PostgreSQL-backed storage for blabby.
//
// schema.sql is the single source of truth for the database schema. It is applied
// both by docker-compose (mounted as an init script for local development) and by
// integration tests (exec'd against a clean database).
//
// This package holds the table repositories — UserRepo, RoomRepo, MembershipRepo,
// VerificationRepo, and the timeline Journal — plus the PendingAccountSweeper. Each
// takes a postgres.Querier (a pool or a transaction) per call, so a caller can
// compose several operations into one transaction. They issue fixed parameterized
// SQL and parse rows into typed value objects at the boundary (parse, don't
// validate), so callers handle domain types, never bare ints or strings.
//
// Subpackage postgres bootstraps the pgxpool connection pool and exposes the
// Querier abstraction. Subpackage workerlease leases the per-node worker id that
// the Journal's Snowflake id source mints from; it is a lifecycle component, kept
// separate from the repositories.
package persistence

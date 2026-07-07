// Package persistence provides PostgreSQL-backed storage for blabby.
//
// schema.sql is the single source of truth for the database schema. It is applied
// both by docker-compose (mounted as an init script for local development) and by
// integration tests (exec'd against a clean database).
//
// Subpackage postgres bootstraps the pgxpool connection pool and exposes the
// Querier abstraction — a pool or a transaction — that repositories accept per
// call so a caller can compose several operations into one transaction. The
// repositories are wired to the live pool and back the gateway handlers and the
// room and user grains.
package persistence

// Package persistence provides PostgreSQL-backed storage for blabby.
//
// schema.sql is the single source of truth for the database schema. It is applied
// both by docker-compose (mounted as an init script for local development) and by
// integration tests (exec'd against a clean database).
//
// Subpackage postgres bootstraps the pgxpool connection pool from configuration.
// The repositories that query the pool are added in later phases; the grains are
// not yet wired to them, so Phase 1's in-memory behavior is unchanged.
package persistence

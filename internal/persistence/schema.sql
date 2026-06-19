-- blabby persistence schema (PostgreSQL + PGroonga).
--
-- This file is the single source of truth for the database schema. It is applied
-- two ways from the same file, so there is no second copy to drift:
--   1. docker-compose mounts it at /docker-entrypoint-initdb.d/ so `make up`
--      provisions a ready database on a fresh volume (demo/local). Init scripts
--      run only on an empty data directory; edit-then-reprovision with
--      `make db-reset`.
--   2. integration tests exec this file against a clean database for isolation.
--
-- Statements are written to be safe to re-run (IF NOT EXISTS / idempotent).
--
-- This phase establishes only the PGroonga extension (infrastructure). The
-- tables, indexes, and seed data are added in the schema phase.

CREATE EXTENSION IF NOT EXISTS pgroonga;

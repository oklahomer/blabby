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
-- Identifiers are Snowflake BIGINTs (one shared number space across users, rooms,
-- and events). Enum-like columns use native PostgreSQL ENUM types — readable
-- predicates (`role = 'owner'`) and DB-level type safety; the Go side maps each to
-- a typed enum. The usual enum-evolution friction does not apply because the schema
-- is recreated from scratch (see header above), never altered in place.

CREATE EXTENSION IF NOT EXISTS pgroonga;

-- Enum types. CREATE TYPE has no IF NOT EXISTS, so each is guarded to stay
-- re-runnable. The Go enums mirror these labels.
DO $$ BEGIN
    CREATE TYPE user_status AS ENUM ('pending', 'active', 'disabled');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE room_status AS ENUM ('active', 'archived');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE membership_role AS ENUM ('owner', 'admin', 'member');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE event_type AS ENUM ('message_posted', 'member_joined', 'member_left');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- service_user: the account entity (named to avoid the reserved word `user`).
-- mail_address and handle_norm store the normalized (lowercased) forms used for
-- lookups; handle keeps the display casing. password_hash is NOT NULL — every
-- account, including seeds, carries a real hash.
CREATE TABLE IF NOT EXISTS service_user (
    id            BIGINT      PRIMARY KEY,
    public_code   TEXT        NOT NULL UNIQUE,
    mail_address  TEXT        NOT NULL UNIQUE,
    handle        TEXT        NOT NULL,
    handle_norm   TEXT        NOT NULL UNIQUE,
    display_name  TEXT        NOT NULL,
    password_hash BYTEA       NOT NULL,
    status        user_status NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- room: display_name is the label (not unique); public_code is the address. The
-- owner is not a column — it is the room_membership row with role = 'owner'.
CREATE TABLE IF NOT EXISTS room (
    id           BIGINT      PRIMARY KEY,
    -- The UNIQUE constraint is named explicitly so roomrepo's collision
    -- classifier (publicCodeConstraint) cannot drift from Postgres's implicit
    -- naming.
    public_code  TEXT        NOT NULL CONSTRAINT room_public_code_key UNIQUE,
    display_name TEXT        NOT NULL,
    created_by   BIGINT      NOT NULL REFERENCES service_user (id),
    status       room_status NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- room_membership: current-state only (a leave deletes the row). The Room grain is
-- the sole writer. The partial unique index enforces at-most-one owner; the
-- at-least-one-owner rule is a domain invariant, not a DB constraint.
CREATE TABLE IF NOT EXISTS room_membership (
    room_id   BIGINT          NOT NULL REFERENCES room (id),
    user_id   BIGINT          NOT NULL REFERENCES service_user (id),
    role      membership_role NOT NULL,
    joined_at TIMESTAMPTZ     NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, user_id)
);
CREATE INDEX IF NOT EXISTS room_membership_user_idx
    ON room_membership (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS room_membership_one_owner_idx
    ON room_membership (room_id) WHERE role = 'owner';

-- event: append-only room timeline journal holding messages and member_joined /
-- member_left system events. The Room grain is the single appender per room, so the
-- Snowflake id is monotonic per room and orders the timeline (occurred_at is
-- display-only).
CREATE TABLE IF NOT EXISTS event (
    id          BIGINT      PRIMARY KEY,
    room_id     BIGINT      NOT NULL REFERENCES room (id),
    type        event_type  NOT NULL,
    user_id     BIGINT      NOT NULL REFERENCES service_user (id),
    occurred_at TIMESTAMPTZ NOT NULL,
    client_key  TEXT,
    payload     JSONB       NOT NULL
);
-- Timeline pagination, newest first.
CREATE INDEX IF NOT EXISTS event_room_timeline_idx
    ON event (room_id, id DESC);
-- Message idempotency: a retried send with the same client_key can't duplicate.
CREATE UNIQUE INDEX IF NOT EXISTS event_client_key_idx
    ON event (room_id, user_id, client_key) WHERE client_key IS NOT NULL;
-- Partial CJK full-text over message text only. Search: (payload->>'text') &@~ $1.
CREATE INDEX IF NOT EXISTS event_message_text_pgroonga_idx
    ON event USING pgroonga ((payload->>'text')) WHERE type = 'message_posted';

-- email_verification: one pending verification per user, deleted on success. The FK
-- cascades so the pending-account GC removes the verification row with the user.
CREATE TABLE IF NOT EXISTS email_verification (
    user_id      BIGINT      PRIMARY KEY REFERENCES service_user (id) ON DELETE CASCADE,
    pin_hash     BYTEA       NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    attempts     INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    resend_count INT         NOT NULL DEFAULT 0 CHECK (resend_count >= 0),
    last_sent_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- worker_lease: one row per Snowflake worker id. lease_token is the fencing token;
-- renewal is conditional on (worker_id, lease_token, not expired).
CREATE TABLE IF NOT EXISTS worker_lease (
    worker_id   INT         PRIMARY KEY CHECK (worker_id BETWEEN 0 AND 1023),
    owner       TEXT        NOT NULL,
    lease_token UUID        NOT NULL,
    acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    renewed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);

-- Deterministic dev fixtures. Fixed ids occupy the low end of the shared Snowflake
-- number space (a real generator mints ids far above these at any real time after
-- the epoch, so they never collide): users 1-3, rooms 4-5. public_codes are fixed
-- bare Crockford codes (the U/R type letter is prepended only at the edge).
-- password_hash is bcrypt(cost 12) over base64(SHA-256(password)) for
-- alice123/bob123/charlie123 — the same scheme the Phase 6 verifier uses, so the
-- seed accounts are loginable and no password_hash is ever NULL.
INSERT INTO service_user (id, public_code, mail_address, handle, handle_norm, display_name, password_hash, status, created_at, updated_at) VALUES
    (1, 'A000000001', 'alice@example.com',   'alice',   'alice',   'alice',   convert_to('$2a$12$zgAytEmdKjaOvd.r0PJUKezVSto9L4PADnVozWu8XVUSuEHf.0GXq', 'UTF8'), 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
    (2, 'B000000002', 'bob@example.com',     'bob',     'bob',     'bob',     convert_to('$2a$12$Du8qPiZd8QDAe6VZAtw1W.UPz0fm.BFu7.NruxB9BzCcbvFoxdgaq', 'UTF8'), 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
    (3, 'C000000003', 'charlie@example.com', 'charlie', 'charlie', 'charlie', convert_to('$2a$12$mTavr5AjYMSn1XeAjqmJp.GReMPazdDfod26WAY4z9UuEqilFKX9q', 'UTF8'), 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
ON CONFLICT DO NOTHING;

INSERT INTO room (id, public_code, display_name, created_by, status, created_at, updated_at) VALUES
    (4, 'G000000004', 'General', 1, 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
    (5, 'H000000005', 'Random',  2, 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
ON CONFLICT DO NOTHING;

-- Each seed room's creator is its owner.
INSERT INTO room_membership (room_id, user_id, role, joined_at) VALUES
    (4, 1, 'owner', '2026-01-01T00:00:00Z'),
    (5, 2, 'owner', '2026-01-01T00:00:00Z')
ON CONFLICT DO NOTHING;

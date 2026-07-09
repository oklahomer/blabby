package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// ErrUserNotFound is returned when a lookup matches no account. The reads load a
// user regardless of status (a pending account still resolves), so this means no
// row carries the given email, handle, public_code, or id at all — letting the
// caller distinguish "no such account" from "found but not yet active".
var ErrUserNotFound = errors.New("persistence: user not found")

// ErrUserPublicCodeCollision reports that a minted public_code collided with an
// existing row. It is recoverable: the caller retries Create — or, when Create
// runs inside a transaction, the whole transaction — so a fresh code is minted.
// Create does not retry in place because a failed INSERT aborts the caller's
// transaction (a 50-bit random code colliding over a small table is rare enough
// that a couple of caller retries always suffice).
var ErrUserPublicCodeCollision = errors.New("persistence: public_code collision")

// ErrMailAddressTaken and ErrHandleTaken report that a Create collided with an
// existing account's email or handle. Unlike a public_code collision they are not
// recoverable by retrying — the caller (registration) maps them to the
// EMAIL_ALREADY_REGISTERED / HANDLE_ALREADY_TAKEN responses.
var (
	ErrMailAddressTaken = errors.New("persistence: mail address already registered")
	ErrHandleTaken      = errors.New("persistence: handle already taken")
)

// uniqueViolation is the SQLSTATE Postgres raises when an INSERT collides with a
// UNIQUE constraint. The constraint names are declared explicitly in schema.sql,
// so classifying a violation by name (not by Postgres's implicit naming) maps each
// duplicate to its specific sentinel — a recoverable public_code clash, or a taken
// email/handle.
const (
	uniqueViolation          = "23505"
	userPublicCodeConstraint = "service_user_public_code_key"
	mailAddressConstraint    = "service_user_mail_address_key"
	handleNormConstraint     = "service_user_handle_norm_key"
)

// userColumns is the fixed projection scanUser expects, in order. status is cast
// to text so it scans into a Go string without registering the user_status enum
// codec on the pool.
const userColumns = `id, public_code, mail_address, handle, handle_norm, display_name, password_hash, status::text, created_at, updated_at`

// UserIDSource mints the next Snowflake id. It is satisfied by the worker-lease
// Manager, which mints only while it holds an unexpired lease (fail-closed).
type UserIDSource interface {
	Next() (int64, error)
}

// UserRepo reads and writes the service_user table. Its methods take a
// postgres.Querier (a pool or a transaction) per call so a caller can compose a
// Create with other writes — e.g. seeding a verification row — in one transaction.
type UserRepo struct {
	ids UserIDSource
}

// NewUserRepo returns a UserRepo that mints user ids from ids.
func NewUserRepo(ids UserIDSource) *UserRepo {
	return &UserRepo{ids: ids}
}

// UserCreateParams carries the caller-supplied fields of a new account. The UserID and
// public_code are minted by Create, not supplied. MailAddress and Handle are
// already parsed domain values; Create derives the storage strings from them.
type UserCreateParams struct {
	MailAddress  domain.MailAddress
	Handle       domain.Handle
	DisplayName  string
	PasswordHash []byte
	Status       domain.UserStatus
}

const userInsertSQL = `
INSERT INTO service_user (id, public_code, mail_address, handle, handle_norm, display_name, password_hash, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::user_status)
RETURNING ` + userColumns

// Create inserts a new account, minting its UserID and a fresh opaque public_code
// in a single INSERT. It does not retry internally: a public_code collision is
// reported as [ErrUserPublicCodeCollision] so the caller can re-run the operation (or
// its enclosing transaction) with a freshly minted code. Retrying in place would
// be unsafe inside a caller's transaction, where the failed INSERT aborts the
// transaction until it is rolled back. A duplicate email or handle is reported as
// [ErrMailAddressTaken] / [ErrHandleTaken]; any other violation (e.g. a duplicate
// minted UserID on the primary key) is a hard error.
func (r *UserRepo) Create(ctx context.Context, q postgres.Querier, params UserCreateParams) (User, error) {
	rawID, err := r.ids.Next()
	if err != nil {
		return User{}, fmt.Errorf("persistence: mint id: %w", err)
	}
	userID, err := id.NewUserID(rawID)
	if err != nil {
		return User{}, fmt.Errorf("persistence: mint id: %w", err)
	}
	code, err := id.NewPublicCode()
	if err != nil {
		return User{}, fmt.Errorf("persistence: mint public_code: %w", err)
	}

	user, err := scanUser(q.QueryRow(ctx, userInsertSQL,
		userID.Int64(), code.String(), params.MailAddress.String(), params.Handle.Display(),
		params.Handle.Normalized(), params.DisplayName, params.PasswordHash, string(params.Status)))
	if err != nil {
		return User{}, classifyCreateError(err)
	}
	return user, nil
}

const findByEmailSQL = `SELECT ` + userColumns + ` FROM service_user WHERE mail_address = $1`

// FindByEmail resolves an email to its account, regardless of status, so
// login can verify credentials (and surface a pending-account hint) and
// registration can detect a duplicate. It returns ErrUserNotFound when no row
// carries the email.
func (r *UserRepo) FindByEmail(ctx context.Context, q postgres.Querier, mail domain.MailAddress) (User, error) {
	return r.findOne(ctx, q, findByEmailSQL, mail.String())
}

const findByHandleNormSQL = `SELECT ` + userColumns + ` FROM service_user WHERE handle_norm = $1`

// FindByHandle resolves a handle to its account, regardless of status — the
// registration duplicate-handle check. It returns ErrUserNotFound when the handle
// is free.
func (r *UserRepo) FindByHandle(ctx context.Context, q postgres.Querier, handle domain.Handle) (User, error) {
	return r.findOne(ctx, q, findByHandleNormSQL, handle.Normalized())
}

const userFindByIDSQL = `SELECT ` + userColumns + ` FROM service_user WHERE id = $1`

// FindByID loads an account by its internal UserID, regardless of status — the
// backend directory's UserID → profile resolve. It returns ErrUserNotFound when no
// row carries the id.
func (r *UserRepo) FindByID(ctx context.Context, q postgres.Querier, userID id.UserID) (User, error) {
	return r.findOne(ctx, q, userFindByIDSQL, userID.Int64())
}

// findOne runs a single-row userColumns SELECT and maps pgx.ErrNoRows to the
// package sentinel. The three FindBy* lookups differ only in their WHERE clause.
func (r *UserRepo) findOne(ctx context.Context, q postgres.Querier, sql string, arg any) (User, error) {
	user, err := scanUser(q.QueryRow(ctx, sql, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("persistence: find: %w", err)
	}
	return user, nil
}

const resolveByPublicCodeSQL = `SELECT id FROM service_user WHERE public_code = $1`

// ResolveByPublicCode maps an opaque public_code to its internal UserID — the
// gateway's U… (JWT sub) → UserID resolution. The mapping is immutable, so callers
// may cache it. It returns ErrUserNotFound when no account carries the code.
func (r *UserRepo) ResolveByPublicCode(ctx context.Context, q postgres.Querier, code id.PublicCode) (id.UserID, error) {
	var raw int64
	err := q.QueryRow(ctx, resolveByPublicCodeSQL, code.String()).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return id.UserID{}, ErrUserNotFound
	}
	if err != nil {
		return id.UserID{}, fmt.Errorf("persistence: resolve public_code: %w", err)
	}
	userID, err := id.NewUserID(raw)
	if err != nil {
		return id.UserID{}, fmt.Errorf("persistence: resolve public_code: %w", err)
	}
	return userID, nil
}

const setStatusSQL = `UPDATE service_user SET status = $2::user_status, updated_at = now() WHERE id = $1`

// SetStatus updates an account's lifecycle status — the pending → active
// transition on verification. It returns ErrUserNotFound when no row carries the
// id, so a caller can tell a no-op update from a successful one.
func (r *UserRepo) SetStatus(ctx context.Context, q postgres.Querier, userID id.UserID, status domain.UserStatus) error {
	tag, err := q.Exec(ctx, setStatusSQL, userID.Int64(), string(status))
	if err != nil {
		return fmt.Errorf("persistence: set status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

const setPasswordHashSQL = `UPDATE service_user SET password_hash = $2, updated_at = now() WHERE id = $1`

// SetPasswordHash replaces an account's stored password hash — the rehash-on-login
// path, when a credential was stored below the current bcrypt target cost. It
// returns ErrUserNotFound when no row carries the id, so a caller can tell a no-op
// update from a successful one.
func (r *UserRepo) SetPasswordHash(ctx context.Context, q postgres.Querier, userID id.UserID, hash []byte) error {
	tag, err := q.Exec(ctx, setPasswordHashSQL, userID.Int64(), hash)
	if err != nil {
		return fmt.Errorf("persistence: set password hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// classifyCreateError maps a failed Create INSERT to the sentinel for the specific
// UNIQUE constraint it violated — a recoverable public_code collision, a taken
// email, or a taken handle — by inspecting the constraint name. Any other error
// (including a primary-key clash on a duplicate minted UserID) is wrapped as a hard
// error so it is never mistaken for a recoverable or expected duplicate.
func classifyCreateError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case userPublicCodeConstraint:
			return ErrUserPublicCodeCollision
		case mailAddressConstraint:
			return ErrMailAddressTaken
		case handleNormConstraint:
			return ErrHandleTaken
		}
	}
	return fmt.Errorf("persistence: create: %w", err)
}

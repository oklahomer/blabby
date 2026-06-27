package userrepo

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
var ErrUserNotFound = errors.New("userrepo: user not found")

// ErrPublicCodeCollision reports that a minted public_code collided with an
// existing row. It is recoverable: the caller retries Create — or, when Create
// runs inside a transaction, the whole transaction — so a fresh code is minted.
// Create does not retry in place because a failed INSERT aborts the caller's
// transaction (a 50-bit random code colliding over a small table is rare enough
// that a couple of caller retries always suffice).
var ErrPublicCodeCollision = errors.New("userrepo: public_code collision")

// uniqueViolation is the SQLSTATE Postgres raises when an INSERT collides with a
// UNIQUE constraint. publicCodeConstraint names the specific constraint on
// service_user.public_code (declared explicitly in schema.sql), so only a code
// clash — not, say, a duplicate email or handle — is classified as a recoverable
// public_code collision.
const (
	uniqueViolation      = "23505"
	publicCodeConstraint = "service_user_public_code_key"
)

// userColumns is the fixed projection scanUser expects, in order. status is cast
// to text so it scans into a Go string without registering the user_status enum
// codec on the pool.
const userColumns = `id, public_code, mail_address, handle, handle_norm, display_name, password_hash, status::text, created_at, updated_at`

// IDSource mints the next Snowflake id. It is satisfied by the worker-lease
// Manager, which mints only while it holds an unexpired lease (fail-closed).
type IDSource interface {
	Next() (int64, error)
}

// Repo reads and writes the service_user table. Its methods take a
// postgres.Querier (a pool or a transaction) per call so a caller can compose a
// Create with other writes — e.g. seeding a verification row — in one transaction.
type Repo struct {
	ids IDSource
}

// New returns a Repo that mints user ids from ids.
func New(ids IDSource) *Repo {
	return &Repo{ids: ids}
}

// CreateParams carries the caller-supplied fields of a new account. The UserID and
// public_code are minted by Create, not supplied. The caller normalizes
// MailAddress and HandleNorm before calling; the repo stores them verbatim.
type CreateParams struct {
	MailAddress  string
	Handle       string
	HandleNorm   string
	DisplayName  string
	PasswordHash []byte
	Status       domain.UserStatus
}

const insertSQL = `
INSERT INTO service_user (id, public_code, mail_address, handle, handle_norm, display_name, password_hash, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::user_status)
RETURNING ` + userColumns

// Create inserts a new account, minting its UserID and a fresh opaque public_code
// in a single INSERT. It does not retry internally: a public_code collision is
// reported as [ErrPublicCodeCollision] so the caller can re-run the operation (or
// its enclosing transaction) with a freshly minted code. Retrying in place would
// be unsafe inside a caller's transaction, where the failed INSERT aborts the
// transaction until it is rolled back. Any other unique violation — a duplicate
// email, handle, or minted UserID — is returned as a hard error.
func (r *Repo) Create(ctx context.Context, q postgres.Querier, params CreateParams) (User, error) {
	rawID, err := r.ids.Next()
	if err != nil {
		return User{}, fmt.Errorf("userrepo: mint id: %w", err)
	}
	userID, err := id.NewUserID(rawID)
	if err != nil {
		return User{}, fmt.Errorf("userrepo: mint id: %w", err)
	}
	code, err := id.NewPublicCode()
	if err != nil {
		return User{}, fmt.Errorf("userrepo: mint public_code: %w", err)
	}

	user, err := scanUser(q.QueryRow(ctx, insertSQL,
		userID.Int64(), code.String(), params.MailAddress, params.Handle,
		params.HandleNorm, params.DisplayName, params.PasswordHash, string(params.Status)))
	switch {
	case err == nil:
		return user, nil
	case isPublicCodeCollision(err):
		return User{}, ErrPublicCodeCollision
	default:
		return User{}, fmt.Errorf("userrepo: create: %w", err)
	}
}

const findByEmailSQL = `SELECT ` + userColumns + ` FROM service_user WHERE mail_address = $1`

// FindByEmail resolves a normalized email to its account, regardless of status, so
// login can verify credentials (and surface a pending-account hint) and
// registration can detect a duplicate. It returns ErrUserNotFound when no row
// carries the email. The caller must pass the same normalization used at insert.
func (r *Repo) FindByEmail(ctx context.Context, q postgres.Querier, mail string) (User, error) {
	return r.findOne(ctx, q, findByEmailSQL, mail)
}

const findByHandleNormSQL = `SELECT ` + userColumns + ` FROM service_user WHERE handle_norm = $1`

// FindByHandleNorm resolves a normalized handle to its account, regardless of
// status — the registration duplicate-handle check. It returns ErrUserNotFound
// when the handle is free.
func (r *Repo) FindByHandleNorm(ctx context.Context, q postgres.Querier, handleNorm string) (User, error) {
	return r.findOne(ctx, q, findByHandleNormSQL, handleNorm)
}

const findByIDSQL = `SELECT ` + userColumns + ` FROM service_user WHERE id = $1`

// FindByID loads an account by its internal UserID, regardless of status — the
// backend directory's UserID → profile resolve. It returns ErrUserNotFound when no
// row carries the id.
func (r *Repo) FindByID(ctx context.Context, q postgres.Querier, userID id.UserID) (User, error) {
	return r.findOne(ctx, q, findByIDSQL, userID.Int64())
}

// findOne runs a single-row userColumns SELECT and maps pgx.ErrNoRows to the
// package sentinel. The three FindBy* lookups differ only in their WHERE clause.
func (r *Repo) findOne(ctx context.Context, q postgres.Querier, sql string, arg any) (User, error) {
	user, err := scanUser(q.QueryRow(ctx, sql, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("userrepo: find: %w", err)
	}
	return user, nil
}

const resolveByPublicCodeSQL = `SELECT id FROM service_user WHERE public_code = $1`

// ResolveByPublicCode maps an opaque public_code to its internal UserID — the
// gateway's U… (JWT sub) → UserID resolution. The mapping is immutable, so callers
// may cache it. It returns ErrUserNotFound when no account carries the code.
func (r *Repo) ResolveByPublicCode(ctx context.Context, q postgres.Querier, code id.PublicCode) (id.UserID, error) {
	var raw int64
	err := q.QueryRow(ctx, resolveByPublicCodeSQL, code.String()).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return id.UserID{}, ErrUserNotFound
	}
	if err != nil {
		return id.UserID{}, fmt.Errorf("userrepo: resolve public_code: %w", err)
	}
	userID, err := id.NewUserID(raw)
	if err != nil {
		return id.UserID{}, fmt.Errorf("userrepo: resolve public_code: %w", err)
	}
	return userID, nil
}

const setStatusSQL = `UPDATE service_user SET status = $2::user_status, updated_at = now() WHERE id = $1`

// SetStatus updates an account's lifecycle status — the pending → active
// transition on verification. It returns ErrUserNotFound when no row carries the
// id, so a caller can tell a no-op update from a successful one.
func (r *Repo) SetStatus(ctx context.Context, q postgres.Querier, userID id.UserID, status domain.UserStatus) error {
	tag, err := q.Exec(ctx, setStatusSQL, userID.Int64(), string(status))
	if err != nil {
		return fmt.Errorf("userrepo: set status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// isPublicCodeCollision reports whether err is (or wraps) a Postgres
// unique_violation on the public_code constraint specifically. Any other unique
// violation (a duplicate email, handle, or primary-key clash) is a different fault
// and must not be retried as a code collision.
func isPublicCodeCollision(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == publicCodeConstraint
}

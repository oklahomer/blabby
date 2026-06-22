package roomrepo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// ErrRoomNotFound is returned when a lookup matches no room. The active-only
// lookups (FindByPublicCode, ListByIDs) also report an archived room as not
// found, matching the gateway's ROOM_NOT_FOUND contract: an inactive room is not
// addressable by its public code. FindByID, which loads regardless of status,
// returns it only when no row carries the id at all.
var ErrRoomNotFound = errors.New("roomrepo: room not found")

// ErrPublicCodeCollision reports that a minted public_code collided with an
// existing row. It is recoverable: the caller retries Create — or, when Create
// runs inside a transaction, the whole transaction — so a fresh code is minted.
// Create does not retry in place because a failed INSERT aborts the caller's
// transaction (a 50-bit random code colliding over a small table is rare enough
// that a couple of caller retries always suffice).
var ErrPublicCodeCollision = errors.New("roomrepo: public_code collision")

// uniqueViolation is the SQLSTATE Postgres raises when an INSERT collides with a
// UNIQUE constraint. publicCodeConstraint names the specific constraint on
// room.public_code (declared explicitly in schema.sql), so only a code clash —
// not, say, a primary-key collision on a duplicate minted RoomID — is classified
// as a recoverable public_code collision.
const (
	uniqueViolation      = "23505"
	publicCodeConstraint = "room_public_code_key"
)

// roomColumns is the fixed projection scanRoom expects, in order. status is cast to
// text so it scans into a Go string without registering the room_status enum codec
// on the pool.
const roomColumns = `id, public_code, display_name, created_by, status::text, created_at, updated_at`

// IDSource mints the next Snowflake id. It is satisfied by the worker-lease
// Manager, which mints only while it holds an unexpired lease (fail-closed).
type IDSource interface {
	Next() (int64, error)
}

// Repo reads and writes the room table. Its methods take a postgres.Querier (a
// pool or a transaction) per call so a caller can compose a Create with other
// writes — e.g. seeding the creator's owner membership — in one transaction.
type Repo struct {
	ids IDSource
}

// New returns a Repo that mints room ids from ids.
func New(ids IDSource) *Repo {
	return &Repo{ids: ids}
}

// CreateParams carries the caller-supplied fields of a new room. The RoomID and
// public_code are minted by Create, not supplied.
type CreateParams struct {
	DisplayName string
	CreatedBy   id.UserID
}

const insertSQL = `
INSERT INTO room (id, public_code, display_name, created_by, status)
VALUES ($1, $2, $3, $4, 'active')
RETURNING ` + roomColumns

// Create inserts a new active room, minting its RoomID and a fresh opaque
// public_code in a single INSERT. It does not retry internally: a public_code
// collision is reported as [ErrPublicCodeCollision] so the caller can re-run the
// operation (or its enclosing transaction) with a freshly minted code. Retrying
// in place would be unsafe inside a caller's transaction, where the failed INSERT
// aborts the transaction until it is rolled back. Any other unique violation —
// e.g. a duplicate minted RoomID on the primary key — is returned as a hard error.
func (r *Repo) Create(ctx context.Context, q postgres.Querier, params CreateParams) (Room, error) {
	rawID, err := r.ids.Next()
	if err != nil {
		return Room{}, fmt.Errorf("roomrepo: mint id: %w", err)
	}
	roomID, err := id.NewRoomID(rawID)
	if err != nil {
		return Room{}, fmt.Errorf("roomrepo: mint id: %w", err)
	}
	code, err := id.NewPublicCode()
	if err != nil {
		return Room{}, fmt.Errorf("roomrepo: mint public_code: %w", err)
	}

	room, err := scanRoom(q.QueryRow(ctx, insertSQL,
		roomID.Int64(), code.String(), params.DisplayName, params.CreatedBy.Int64()))
	switch {
	case err == nil:
		return room, nil
	case isPublicCodeCollision(err):
		return Room{}, ErrPublicCodeCollision
	default:
		return Room{}, fmt.Errorf("roomrepo: create: %w", err)
	}
}

const findByCodeSQL = `SELECT ` + roomColumns + ` FROM room WHERE public_code = $1 AND status = 'active'`

// FindByPublicCode resolves an opaque public_code to its active room (the
// gateway's R…→RoomID lookup). It returns ErrRoomNotFound when no active room
// carries the code.
func (r *Repo) FindByPublicCode(ctx context.Context, q postgres.Querier, code id.PublicCode) (Room, error) {
	room, err := scanRoom(q.QueryRow(ctx, findByCodeSQL, code.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return Room{}, ErrRoomNotFound
	}
	if err != nil {
		return Room{}, fmt.Errorf("roomrepo: find by public_code: %w", err)
	}
	return room, nil
}

const findByIDSQL = `SELECT ` + roomColumns + ` FROM room WHERE id = $1`

// FindByID loads a room by its internal RoomID regardless of status, so the Room
// grain can hydrate its own metadata on activation and see an archived room (to
// reject commands) rather than treating it as never having existed. It returns
// ErrRoomNotFound only when no row carries the id. This is distinct from
// FindByPublicCode, which is active-only because an archived room is not
// addressable by its public code.
func (r *Repo) FindByID(ctx context.Context, q postgres.Querier, roomID id.RoomID) (Room, error) {
	room, err := scanRoom(q.QueryRow(ctx, findByIDSQL, roomID.Int64()))
	if errors.Is(err, pgx.ErrNoRows) {
		return Room{}, ErrRoomNotFound
	}
	if err != nil {
		return Room{}, fmt.Errorf("roomrepo: find by id: %w", err)
	}
	return room, nil
}

const listByIDsSQL = `SELECT ` + roomColumns + ` FROM room WHERE id = ANY($1) AND status = 'active'`

// ListByIDs returns the rooms with the given internal ids, in no particular order
// (the caller re-associates by id). It is the gateway's internal-RoomID → R…
// descriptor mapping for the joined-rooms response. Unknown or archived ids are
// simply absent from the result; an empty input yields an empty result.
func (r *Repo) ListByIDs(ctx context.Context, q postgres.Querier, ids []id.RoomID) ([]Room, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	raw := make([]int64, len(ids))
	for i, roomID := range ids {
		raw[i] = roomID.Int64()
	}
	rows, err := q.Query(ctx, listByIDsSQL, raw)
	if err != nil {
		return nil, fmt.Errorf("roomrepo: list by ids: %w", err)
	}
	return collectRooms(rows)
}

const listActiveSQL = `SELECT ` + roomColumns + ` FROM room WHERE status = 'active' ORDER BY id`

// ListActive returns active rooms ordered by id — the gateway's room catalogue. A
// positive limit caps the result; a non-positive limit returns all active rooms.
// (Substring filtering and keyset pagination arrive with the discovery work.)
func (r *Repo) ListActive(ctx context.Context, q postgres.Querier, limit int) ([]Room, error) {
	query := listActiveSQL
	var args []any
	if limit > 0 {
		query += " LIMIT $1"
		args = append(args, limit)
	}
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("roomrepo: list active: %w", err)
	}
	return collectRooms(rows)
}

// collectRooms scans and parses every row, closing the rows on return.
func collectRooms(rows pgx.Rows) ([]Room, error) {
	defer rows.Close()
	var out []Room
	for rows.Next() {
		room, err := scanRoom(rows)
		if err != nil {
			return nil, fmt.Errorf("roomrepo: scan: %w", err)
		}
		out = append(out, room)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("roomrepo: rows: %w", err)
	}
	return out, nil
}

// isPublicCodeCollision reports whether err is (or wraps) a Postgres
// unique_violation on the public_code constraint specifically. Any other unique
// violation (e.g. a primary-key clash) is a different fault and must not be
// retried as a code collision.
func isPublicCodeCollision(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == publicCodeConstraint
}

package membershiprepo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// ErrMembershipNotFound reports that Remove targeted a (room, user) pair with no
// membership row. The Room grain gates duplicate-leave idempotency on its cache
// before calling Remove, so reaching here means the cache and the DB diverged;
// the caller fails closed (rolls back the transaction) rather than append a
// member_left event for a change that did not happen.
var ErrMembershipNotFound = errors.New("membershiprepo: membership not found")

// Repo reads and writes the room_membership table. Like the sibling repos its
// methods take a postgres.Querier (pool or tx) per call, so the Room grain can
// compose an Add/Remove with a journal append in one transaction.
//
// It has no id source: a membership row's identity is the (room_id, user_id)
// composite key, nothing is minted.
type Repo struct{}

// New returns a Repo.
func New() *Repo { return &Repo{} }

const listByRoomSQL = `
SELECT m.user_id, u.display_name, m.role::text, m.joined_at
FROM room_membership m
JOIN service_user u ON u.id = m.user_id
WHERE m.room_id = $1
ORDER BY m.joined_at, m.user_id`

// ListByRoom returns the room's members (joined to service_user for the display
// name), ordered by join time then id for deterministic fan-out. The Room grain
// calls this on activation to seed its member cache.
func (r *Repo) ListByRoom(ctx context.Context, q postgres.Querier, roomID id.RoomID) ([]Member, error) {
	rows, err := q.Query(ctx, listByRoomSQL, roomID.Int64())
	if err != nil {
		return nil, fmt.Errorf("membershiprepo: list by room: %w", err)
	}
	return collectMembers(rows)
}

const listByUserSQL = `
SELECT r.id, r.public_code, r.display_name, r.status::text, r.updated_at
FROM room_membership m
JOIN room r ON r.id = m.room_id
WHERE m.user_id = $1 AND r.status = 'active'
ORDER BY r.id`

// ListByUser returns the active rooms the user belongs to as RoomRef descriptors
// (joined to room for public_code/name/status), ordered by id. The User grain
// hydrates its joined-rooms cache from this on activation. Archived rooms are
// omitted — an inactive room is not a usable joined room (mirrors the active-only
// reads in roomrepo).
func (r *Repo) ListByUser(ctx context.Context, q postgres.Querier, userID id.UserID) ([]domain.RoomRef, error) {
	rows, err := q.Query(ctx, listByUserSQL, userID.Int64())
	if err != nil {
		return nil, fmt.Errorf("membershiprepo: list by user: %w", err)
	}
	return collectJoinedRooms(rows)
}

const addSQL = `INSERT INTO room_membership (room_id, user_id, role) VALUES ($1, $2, $3::membership_role)`

// Add inserts a membership row. The Room grain calls this only after confirming
// from its cache that the user is not already a member, so a duplicate (the
// composite PK) is a contract violation surfaced as a hard error, not silenced.
func (r *Repo) Add(ctx context.Context, q postgres.Querier, roomID id.RoomID, ref id.UserRef, role domain.MembershipRole) error {
	if _, err := q.Exec(ctx, addSQL, roomID.Int64(), ref.ID().Int64(), string(role)); err != nil {
		return fmt.Errorf("membershiprepo: add: %w", err)
	}
	return nil
}

const removeSQL = `DELETE FROM room_membership WHERE room_id = $1 AND user_id = $2`

// Remove deletes a membership row, returning ErrMembershipNotFound when no row
// matched. The Room grain gates duplicate-leave idempotency on its cache, so it
// only calls Remove on a real member->non-member transition where the row must
// exist; a 0-row delete is therefore a cache/DB divergence the caller fails
// closed on, not a benign no-op. This keeps the membership row and its derived
// member_left event committing together.
func (r *Repo) Remove(ctx context.Context, q postgres.Querier, roomID id.RoomID, userID id.UserID) error {
	tag, err := q.Exec(ctx, removeSQL, roomID.Int64(), userID.Int64())
	if err != nil {
		return fmt.Errorf("membershiprepo: remove: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMembershipNotFound
	}
	return nil
}

// collectMembers scans and parses every row, closing the rows on return.
func collectMembers(rows pgx.Rows) ([]Member, error) {
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var mr memberRow
		if err := rows.Scan(&mr.userID, &mr.displayName, &mr.role, &mr.joinedAt); err != nil {
			return nil, fmt.Errorf("membershiprepo: scan member: %w", err)
		}
		member, err := mr.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("membershiprepo: rows: %w", err)
	}
	return out, nil
}

// collectJoinedRooms scans and parses every row, closing the rows on return.
func collectJoinedRooms(rows pgx.Rows) ([]domain.RoomRef, error) {
	defer rows.Close()
	var out []domain.RoomRef
	for rows.Next() {
		var jr joinedRoomRow
		if err := rows.Scan(&jr.roomID, &jr.publicCode, &jr.name, &jr.status, &jr.updatedAt); err != nil {
			return nil, fmt.Errorf("membershiprepo: scan joined room: %w", err)
		}
		ref, err := jr.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("membershiprepo: rows: %w", err)
	}
	return out, nil
}

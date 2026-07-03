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

// ErrMembershipNotFound reports that a mutation targeted a (room, user) pair with
// no membership row. The Room grain gates membership preconditions on its cache
// before calling the repo, so reaching here means the cache and the DB diverged;
// the caller fails closed (rolls back the transaction) rather than act on a state
// that does not hold.
var ErrMembershipNotFound = errors.New("membershiprepo: membership not found")

// ErrOwnerCannotLeave reports that Remove targeted the room's owner. Ownership
// must be transferred before the owner can leave, so the row is kept and the
// caller's transaction rolls back.
var ErrOwnerCannotLeave = errors.New("membershiprepo: the owner cannot leave the room")

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

// removeSQL deletes the membership row unless it holds the owner role, and
// reports which precondition failed: the CTE observes whether the row existed at
// all, the guarded DELETE whether it was removable. Deciding both in one
// statement keeps the owner check and the delete atomic without a
// read-then-write.
const removeSQL = `
WITH target AS (
    SELECT 1 FROM room_membership WHERE room_id = $1 AND user_id = $2
),
deleted AS (
    DELETE FROM room_membership
    WHERE room_id = $1 AND user_id = $2 AND role <> 'owner'
    RETURNING 1
)
SELECT EXISTS(SELECT 1 FROM target), EXISTS(SELECT 1 FROM deleted)`

// Remove deletes a membership row. It returns ErrMembershipNotFound when no row
// matched and ErrOwnerCannotLeave when the row holds the owner role — ownership
// must be transferred before the owner can leave, so the room is never left
// ownerless. The Room grain gates duplicate-leave idempotency on its cache, so a
// missing row is a cache/DB divergence the caller fails closed on, not a benign
// no-op. This keeps the membership row and its derived member_left event
// committing together.
func (r *Repo) Remove(ctx context.Context, q postgres.Querier, roomID id.RoomID, userID id.UserID) error {
	var existed, deleted bool
	if err := q.QueryRow(ctx, removeSQL, roomID.Int64(), userID.Int64()).Scan(&existed, &deleted); err != nil {
		return fmt.Errorf("membershiprepo: remove: %w", err)
	}
	switch {
	case !existed:
		return ErrMembershipNotFound
	case !deleted:
		return ErrOwnerCannotLeave
	default:
		return nil
	}
}

const getRoleSQL = `SELECT role::text FROM room_membership WHERE room_id = $1 AND user_id = $2`

// GetRole returns the member's role, or ErrMembershipNotFound when the user is
// not a member. Role-mutation callers read the acting and target members' roles
// with this inside the same transaction as the mutation, so the authorization
// decision and the write commit together.
func (r *Repo) GetRole(ctx context.Context, q postgres.Querier, roomID id.RoomID, userID id.UserID) (domain.MembershipRole, error) {
	var raw string
	err := q.QueryRow(ctx, getRoleSQL, roomID.Int64(), userID.Int64()).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrMembershipNotFound
	}
	if err != nil {
		return "", fmt.Errorf("membershiprepo: get role: %w", err)
	}
	role, err := domain.ParseMembershipRole(raw)
	if err != nil {
		return "", fmt.Errorf("membershiprepo: get role: %w", err)
	}
	return role, nil
}

const updateRoleSQL = `UPDATE room_membership SET role = $3::membership_role WHERE room_id = $1 AND user_id = $2`

// UpdateRole sets the member's role, returning ErrMembershipNotFound when no row
// matched. It is a plain table write: the role-change policy (who may set which
// role; the owner role never moves through here) is enforced by the caller —
// see domain.CanSetRole — inside the same transaction as a GetRole read.
// TransferOwnership is the only operation that mutates the owner role.
func (r *Repo) UpdateRole(ctx context.Context, q postgres.Querier, roomID id.RoomID, userID id.UserID, role domain.MembershipRole) error {
	tag, err := q.Exec(ctx, updateRoleSQL, roomID.Int64(), userID.Int64(), string(role))
	if err != nil {
		return fmt.Errorf("membershiprepo: update role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMembershipNotFound
	}
	return nil
}

// transferOwnershipSQL moves the owner role in one statement: the target's
// membership gates the demote (via target), and the demote gates the promote
// (via demoted), so the two role writes land together or not at all — atomic
// even when q is an autocommitting pool rather than a transaction. Each CTE
// reading its predecessor also forces demote-before-promote execution, keeping
// the at-most-one-owner partial unique index satisfied at every point.
const transferOwnershipSQL = `
WITH target AS (
    SELECT 1 FROM room_membership WHERE room_id = $1 AND user_id = $3
),
demoted AS (
    UPDATE room_membership SET role = 'admin'
    WHERE room_id = $1 AND user_id = $2 AND role = 'owner'
      AND EXISTS (SELECT 1 FROM target)
    RETURNING 1
),
promoted AS (
    UPDATE room_membership SET role = 'owner'
    WHERE room_id = $1 AND user_id = $3
      AND EXISTS (SELECT 1 FROM demoted)
    RETURNING 1
)
SELECT EXISTS(SELECT 1 FROM target), EXISTS(SELECT 1 FROM promoted)`

// TransferOwnership moves the owner role from one member to another: the current
// owner is demoted to admin (they keep management rights) and the target is
// promoted, in one all-or-nothing statement. The caller validates the actor's
// authority (and any policy) beforehand — see the MembershipStore adapter — so a
// precondition failing here (from does not own the room, to is not a member) is
// a broken contract surfaced as a hard error, with neither row changed.
func (r *Repo) TransferOwnership(ctx context.Context, q postgres.Querier, roomID id.RoomID, from, to id.UserID) error {
	var targetExists, promoted bool
	if err := q.QueryRow(ctx, transferOwnershipSQL, roomID.Int64(), from.Int64(), to.Int64()).Scan(&targetExists, &promoted); err != nil {
		return fmt.Errorf("membershiprepo: transfer ownership: %w", err)
	}
	switch {
	case !targetExists:
		return fmt.Errorf("membershiprepo: transfer ownership: user %s is not a member of room %s", to, roomID)
	case !promoted:
		return fmt.Errorf("membershiprepo: transfer ownership: user %s does not own room %s", from, roomID)
	default:
		return nil
	}
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

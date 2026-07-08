// The room_membership table is the DB-authoritative record of who belongs to which
// room and in what role. The Room grain is the sole writer (add/remove on a real
// join/leave transition); the User grain only reads its joined rooms. Current-state
// only: a leave deletes the row. Rows are parsed into typed value objects at the
// boundary (parse, don't validate), so callers handle UserID/RoomRef/MembershipRole,
// never bare ints or strings.

package persistence

import (
	"fmt"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

// Member is the room-centric view of one membership row, joined to service_user
// for the member's display name: the identity ref plus the relationship metadata.
// The Room grain loads these on activation to seed its member cache.
type Member struct {
	User     domain.UserRef
	Role     domain.MembershipRole
	JoinedAt time.Time
}

// memberRow is the raw room_membership⋈service_user row in scan order; toDomain
// parses it into a Member, enforcing the id/code/role invariants at the boundary.
type memberRow struct {
	userID      int64
	publicCode  string
	displayName string
	role        string
	joinedAt    time.Time
}

func (mr memberRow) toDomain() (Member, error) {
	userID, err := id.NewUserID(mr.userID)
	if err != nil {
		return Member{}, fmt.Errorf("persistence: row user_id: %w", err)
	}
	code, err := id.ParsePublicCode(mr.publicCode)
	if err != nil {
		return Member{}, fmt.Errorf("persistence: row public_code: %w", err)
	}
	ref, err := domain.NewUserRef(userID, code, mr.displayName)
	if err != nil {
		return Member{}, fmt.Errorf("persistence: row user_ref: %w", err)
	}
	role, err := domain.ParseMembershipRole(mr.role)
	if err != nil {
		return Member{}, fmt.Errorf("persistence: row role: %w", err)
	}
	return Member{User: ref, Role: role, JoinedAt: mr.joinedAt}, nil
}

// joinedRoomRow is the raw room_membership⋈room row in scan order; toDomain parses
// it into the room's RoomRef (the user-centric joined-room view).
type joinedRoomRow struct {
	roomID     int64
	publicCode string
	name       string
	status     string
	updatedAt  time.Time
}

func (jr joinedRoomRow) toDomain() (domain.RoomRef, error) {
	roomID, err := id.NewRoomID(jr.roomID)
	if err != nil {
		return domain.RoomRef{}, fmt.Errorf("persistence: row room_id: %w", err)
	}
	code, err := id.ParsePublicCode(jr.publicCode)
	if err != nil {
		return domain.RoomRef{}, fmt.Errorf("persistence: row public_code: %w", err)
	}
	status, err := domain.ParseRoomStatus(jr.status)
	if err != nil {
		return domain.RoomRef{}, fmt.Errorf("persistence: row status: %w", err)
	}
	ref, err := domain.NewRoomRef(domain.RoomRefParams{
		ID:         roomID,
		PublicCode: code,
		Name:       jr.name,
		Status:     status,
		// MetadataVersion mirrors the Room grain's RoomLoader: updated_at at
		// microsecond precision, so two metadata writes in the same millisecond
		// don't collapse to one version under a receiver's "ignore older" check.
		MetadataVersion: jr.updatedAt.UnixMicro(),
	})
	if err != nil {
		return domain.RoomRef{}, fmt.Errorf("persistence: row room ref: %w", err)
	}
	return ref, nil
}

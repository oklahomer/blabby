package domain

import (
	"fmt"
	"time"

	"github.com/oklahomer/blabby/internal/id"
)

// MembershipRole is a member's role in a room, mirroring the membership_role SQL
// enum. Owner implies admin powers; the at-most-one-owner rule is enforced by the
// room, not by this type.
type MembershipRole string

const (
	// MembershipRoleOwner is the single owner of a room.
	MembershipRoleOwner MembershipRole = "owner"
	// MembershipRoleAdmin can manage members and roles below owner.
	MembershipRoleAdmin MembershipRole = "admin"
	// MembershipRoleMember is an ordinary participant.
	MembershipRoleMember MembershipRole = "member"
)

// ParseMembershipRole parses a raw membership_role label (e.g. a DB enum value)
// into a MembershipRole, rejecting unknown values.
func ParseMembershipRole(raw string) (MembershipRole, error) {
	switch r := MembershipRole(raw); r {
	case MembershipRoleOwner, MembershipRoleAdmin, MembershipRoleMember:
		return r, nil
	default:
		return "", fmt.Errorf("domain: unknown membership role %q", raw)
	}
}

// CanSetRole reports whether a member holding actorRole may change another member
// holding targetRole to newRole. The owner role never changes hands through a
// role change — that is TransferOwnership — so any owner involvement on the
// target side disqualifies the change. Owners and admins may otherwise set the
// roles below owner (promote member→admin, demote admin→member) alike; the owner
// outranks admins only in that TransferOwnership is owner-only.
func CanSetRole(actorRole, targetRole, newRole MembershipRole) bool {
	if targetRole == MembershipRoleOwner || newRole == MembershipRoleOwner {
		return false
	}
	return actorRole == MembershipRoleOwner || actorRole == MembershipRoleAdmin
}

// CanTransferOwnership reports whether a member holding actorRole may transfer
// the room's ownership. Only the owner may hand the room over.
func CanTransferOwnership(actorRole MembershipRole) bool {
	return actorRole == MembershipRoleOwner
}

// MembershipRef is the room-centric view of one membership: who the member is
// (UserRef) plus their relationship metadata. The Room grain caches these keyed by
// UserID, so it can fan out messages and member events with sender/member public
// code, display name, and role without querying the User grain per message.
//
// It does not carry a RoomID (the owning Room grain already knows the room) nor a
// top-level UserID (the map key and User.ID carry that identity).
type MembershipRef struct {
	User            UserRef
	Role            MembershipRole
	JoinedAt        time.Time
	MetadataVersion int64
}

// JoinedRoomRef is the user-centric mirror of MembershipRef: which room was joined
// (RoomRef) plus the relationship metadata. The User grain caches these keyed by
// RoomID, so GetJoinedRooms can return room descriptors and membership metadata
// directly without a room-repository lookup per request.
type JoinedRoomRef struct {
	Room            RoomRef
	Role            MembershipRole
	JoinedAt        time.Time
	MetadataVersion int64
}

// MembershipRecord is a self-identifying membership row: it carries both ids
// because it is not nested under a room or user. Use it for database rows, batch
// query results, audit logs, or queue/event payloads, where the relationship must
// identify itself.
type MembershipRecord struct {
	RoomID          id.RoomID
	UserID          id.UserID
	Role            MembershipRole
	JoinedAt        time.Time
	MetadataVersion int64
}

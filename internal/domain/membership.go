package domain

import (
	"fmt"
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

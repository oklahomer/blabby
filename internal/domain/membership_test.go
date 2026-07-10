package domain_test

import (
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestCanSetRole(t *testing.T) {
	t.Parallel()
	const (
		owner  = domain.MembershipRoleOwner
		admin  = domain.MembershipRoleAdmin
		member = domain.MembershipRoleMember
	)
	tests := []struct {
		name                        string
		actorRole, targetRole, next domain.MembershipRole
		want                        bool
	}{
		{"owner promotes member to admin", owner, member, admin, true},
		{"owner demotes admin to member", owner, admin, member, true},
		{"admin promotes member to admin", admin, member, admin, true},
		{"admin demotes admin to member", admin, admin, member, true},
		{"member may not set roles", member, member, admin, false},
		{"nobody grants owner via role change", owner, member, owner, false},
		{"the owner's own role never moves here", owner, owner, admin, false},
		{"admin cannot touch the owner", admin, owner, member, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.CanSetRole(tc.actorRole, tc.targetRole, tc.next); got != tc.want {
				t.Errorf("CanSetRole(%s, %s, %s) = %v, want %v",
					tc.actorRole, tc.targetRole, tc.next, got, tc.want)
			}
		})
	}
}

func TestCanTransferOwnership(t *testing.T) {
	t.Parallel()
	if !domain.CanTransferOwnership(domain.MembershipRoleOwner) {
		t.Error("the owner must be able to transfer ownership")
	}
	for _, role := range []domain.MembershipRole{domain.MembershipRoleAdmin, domain.MembershipRoleMember} {
		if domain.CanTransferOwnership(role) {
			t.Errorf("%s must not be able to transfer ownership", role)
		}
	}
}

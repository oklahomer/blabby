package domain_test

import (
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

func TestParseRoomStatus(t *testing.T) {
	for _, want := range []domain.RoomStatus{domain.RoomStatusActive, domain.RoomStatusArchived} {
		got, err := domain.ParseRoomStatus(string(want))
		if err != nil || got != want {
			t.Errorf("ParseRoomStatus(%q) = %q, %v; want %q, nil", want, got, err, want)
		}
	}
	if _, err := domain.ParseRoomStatus("bogus"); err == nil {
		t.Error("ParseRoomStatus(bogus) = nil error, want rejection")
	}
}

func TestParseUserStatus(t *testing.T) {
	for _, want := range []domain.UserStatus{domain.UserStatusPending, domain.UserStatusActive, domain.UserStatusDisabled} {
		got, err := domain.ParseUserStatus(string(want))
		if err != nil || got != want {
			t.Errorf("ParseUserStatus(%q) = %q, %v; want %q, nil", want, got, err, want)
		}
	}
	if _, err := domain.ParseUserStatus(""); err == nil {
		t.Error("ParseUserStatus(empty) = nil error, want rejection")
	}
}

func TestParseMembershipRole(t *testing.T) {
	for _, want := range []domain.MembershipRole{domain.MembershipRoleOwner, domain.MembershipRoleAdmin, domain.MembershipRoleMember} {
		got, err := domain.ParseMembershipRole(string(want))
		if err != nil || got != want {
			t.Errorf("ParseMembershipRole(%q) = %q, %v; want %q, nil", want, got, err, want)
		}
	}
	if _, err := domain.ParseMembershipRole("moderator"); err == nil {
		t.Error("ParseMembershipRole(moderator) = nil error, want rejection")
	}
}

func TestRefPublicIDs(t *testing.T) {
	code, err := id.ParsePublicCode("G000000004")
	if err != nil {
		t.Fatalf("ParsePublicCode: %v", err)
	}
	if got := (domain.RoomRef{PublicCode: code}).PublicID(); got != "RG000000004" {
		t.Errorf("RoomRef.PublicID() = %q, want RG000000004", got)
	}
	if got := (domain.UserRef{PublicCode: code}).PublicID(); got != "UG000000004" {
		t.Errorf("UserRef.PublicID() = %q, want UG000000004", got)
	}
}

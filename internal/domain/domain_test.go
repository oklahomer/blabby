package domain_test

import (
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

func mustRoomID(t *testing.T, raw int64) id.RoomID {
	t.Helper()
	r, err := id.NewRoomID(raw)
	if err != nil {
		t.Fatalf("NewRoomID(%d): %v", raw, err)
	}
	return r
}

func mustPublicCode(t *testing.T, raw string) id.PublicCode {
	t.Helper()
	c, err := id.ParsePublicCode(raw)
	if err != nil {
		t.Fatalf("ParsePublicCode(%q): %v", raw, err)
	}
	return c
}

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

func TestNewRoomRef(t *testing.T) {
	valid := domain.RoomRefParams{
		ID:              mustRoomID(t, 1),
		PublicCode:      mustPublicCode(t, "G000000004"),
		Name:            "General",
		Status:          domain.RoomStatusActive,
		MetadataVersion: 42,
	}

	tests := []struct {
		name     string
		mutate   func(p domain.RoomRefParams) domain.RoomRefParams
		wantErr  bool
		wantName string // expected Name() on success (post-trim)
	}{
		{name: "valid", mutate: func(p domain.RoomRefParams) domain.RoomRefParams { return p }, wantName: "General"},
		{name: "name is trimmed", mutate: func(p domain.RoomRefParams) domain.RoomRefParams { p.Name = "  General  "; return p }, wantName: "General"},
		{name: "blank name rejected", mutate: func(p domain.RoomRefParams) domain.RoomRefParams { p.Name = "   "; return p }, wantErr: true},
		{name: "over-long name rejected", mutate: func(p domain.RoomRefParams) domain.RoomRefParams {
			p.Name = strings.Repeat("x", domain.MaxRoomNameBytes+1)
			return p
		}, wantErr: true},
		{name: "zero-value id rejected", mutate: func(p domain.RoomRefParams) domain.RoomRefParams { p.ID = id.RoomID{}; return p }, wantErr: true},
		// The public code is load-bearing: it is the only room identity that
		// crosses to a client, so a ref cannot exist without one.
		{name: "zero-value public code rejected", mutate: func(p domain.RoomRefParams) domain.RoomRefParams { p.PublicCode = id.PublicCode{}; return p }, wantErr: true},
		{name: "unknown status rejected", mutate: func(p domain.RoomRefParams) domain.RoomRefParams { p.Status = "bogus"; return p }, wantErr: true},
		{name: "empty status rejected", mutate: func(p domain.RoomRefParams) domain.RoomRefParams { p.Status = ""; return p }, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.mutate(valid)
			ref, err := domain.NewRoomRef(p)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got ref %+v", ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ref.ID() != p.ID {
				t.Errorf("ID() = %v, want %v", ref.ID(), p.ID)
			}
			if ref.PublicCode() != p.PublicCode {
				t.Errorf("PublicCode() = %v, want %v", ref.PublicCode(), p.PublicCode)
			}
			if ref.Name() != tc.wantName {
				t.Errorf("Name() = %q, want %q", ref.Name(), tc.wantName)
			}
			if ref.Status() != p.Status {
				t.Errorf("Status() = %q, want %q", ref.Status(), p.Status)
			}
			if ref.MetadataVersion() != p.MetadataVersion {
				t.Errorf("MetadataVersion() = %d, want %d", ref.MetadataVersion(), p.MetadataVersion)
			}
		})
	}
}

func TestRefPublicIDs(t *testing.T) {
	code := mustPublicCode(t, "G000000004")
	roomRef, err := domain.NewRoomRef(domain.RoomRefParams{
		ID:         mustRoomID(t, 1),
		PublicCode: code,
		Name:       "General",
		Status:     domain.RoomStatusActive,
	})
	if err != nil {
		t.Fatalf("NewRoomRef: %v", err)
	}
	if got := roomRef.PublicID(); got != "RG000000004" {
		t.Errorf("RoomRef.PublicID() = %q, want RG000000004", got)
	}
	userRef, err := domain.NewUserRef(mustUserID(t, 1), code, "Alice")
	if err != nil {
		t.Fatalf("NewUserRef: %v", err)
	}
	if got := userRef.PublicID(); got != "UG000000004" {
		t.Errorf("UserRef.PublicID() = %q, want UG000000004", got)
	}
}

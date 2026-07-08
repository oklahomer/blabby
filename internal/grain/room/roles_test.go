package room_test

import (
	"reflect"
	"testing"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/grain/room"
	graintest "github.com/oklahomer/blabby/internal/testutil/grain"
)

// rolesGrain builds a loaded grain whose member cache holds Alice (1) and Bob
// (2), backed by store.
func rolesGrain(t *testing.T, store room.MembershipStore) *room.Grain {
	t.Helper()
	g := &room.Grain{}
	g.SetNotifier(&fakeNotifier{})
	g.SetLoader(seededLoader(roomRef(t, testRoomID, domain.RoomStatusActive)))
	g.SetMembershipStore(store)
	g.UseSyncFanout()
	g.Init(fakeRoomCtx(testRoomID))
	return g
}

func aliceAndBob(t *testing.T) []domain.UserRef {
	t.Helper()
	return []domain.UserRef{userRefFor(t, "1", "Alice"), userRefFor(t, "2", "Bob")}
}

func protoActor(id, name string) *commonpb.UserRef {
	return &commonpb.UserRef{Id: id, Name: name, PublicCode: graintest.BarePublicCodeFor(id)}
}

func TestGrain_SetMemberRole_RecordsChange(t *testing.T) {
	store := &fakeMembershipStore{loaded: aliceAndBob(t)}
	g := rolesGrain(t, store)

	resp, err := g.SetMemberRole(&roompb.SetMemberRoleRequest{
		Actor:        protoActor("1", "Alice"),
		TargetUserId: "2",
		Role:         "admin",
	}, fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("expected success, got error: %+v", resp.GetError())
	}
	want := []roleChangeCall{{actor: mustUserID(t, "1"), target: mustUserID(t, "2"), role: domain.MembershipRoleAdmin}}
	if !reflect.DeepEqual(store.roleCalls, want) {
		t.Errorf("RecordRoleChange calls: got %+v, want %+v", store.roleCalls, want)
	}
}

func TestGrain_SetMemberRole_Rejections(t *testing.T) {
	tests := []struct {
		name       string
		req        *roompb.SetMemberRoleRequest
		storeErr   error
		wantCode   int32
		wantStatus string
	}{
		{
			name:       "missing actor",
			req:        &roompb.SetMemberRoleRequest{TargetUserId: "2", Role: "admin"},
			wantCode:   4001,
			wantStatus: "INVALID_REQUEST",
		},
		{
			name:       "missing target",
			req:        &roompb.SetMemberRoleRequest{Actor: protoActor("1", "Alice"), Role: "admin"},
			wantCode:   4001,
			wantStatus: "INVALID_REQUEST",
		},
		{
			name:       "owner role never moves through a role change",
			req:        &roompb.SetMemberRoleRequest{Actor: protoActor("1", "Alice"), TargetUserId: "2", Role: "owner"},
			wantCode:   4001,
			wantStatus: "INVALID_REQUEST",
		},
		{
			name:       "unknown role label",
			req:        &roompb.SetMemberRoleRequest{Actor: protoActor("1", "Alice"), TargetUserId: "2", Role: "superuser"},
			wantCode:   4001,
			wantStatus: "INVALID_REQUEST",
		},
		{
			name:       "actor not a member",
			req:        &roompb.SetMemberRoleRequest{Actor: protoActor("9", "Mallory"), TargetUserId: "2", Role: "admin"},
			wantCode:   2001,
			wantStatus: "ROOM_NOT_MEMBER",
		},
		{
			name:       "target not a member",
			req:        &roompb.SetMemberRoleRequest{Actor: protoActor("1", "Alice"), TargetUserId: "9", Role: "admin"},
			wantCode:   2001,
			wantStatus: "ROOM_NOT_MEMBER",
		},
		{
			name:       "policy refusal maps to permission denied",
			req:        &roompb.SetMemberRoleRequest{Actor: protoActor("1", "Alice"), TargetUserId: "2", Role: "admin"},
			storeErr:   room.ErrRolePermissionDenied,
			wantCode:   2005,
			wantStatus: "ROOM_PERMISSION_DENIED",
		},
		{
			name:       "store failure maps to internal error",
			req:        &roompb.SetMemberRoleRequest{Actor: protoActor("1", "Alice"), TargetUserId: "2", Role: "admin"},
			storeErr:   errFake("db down"),
			wantCode:   5001,
			wantStatus: "INTERNAL_ERROR",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeMembershipStore{loaded: aliceAndBob(t), roleErr: tc.storeErr}
			g := rolesGrain(t, store)

			resp, err := g.SetMemberRole(tc.req, fakeRoomCtx(testRoomID))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertErrResponse(t, resp.GetError(), tc.wantCode, tc.wantStatus)
			if tc.storeErr == nil && len(store.roleCalls) != 0 {
				t.Errorf("store reached on a pre-store rejection: %+v", store.roleCalls)
			}
		})
	}
}

func TestGrain_SetMemberRole_UnloadedRoom(t *testing.T) {
	g := &room.Grain{}
	g.SetNotifier(&fakeNotifier{})
	g.SetLoader(seededLoader()) // no room -> activation leaves the grain unloaded
	g.UseSyncFanout()
	g.Init(fakeRoomCtx(testRoomID))

	resp, err := g.SetMemberRole(&roompb.SetMemberRoleRequest{
		Actor: protoActor("1", "Alice"), TargetUserId: "2", Role: "admin",
	}, fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertErrResponse(t, resp.GetError(), 2003, "ROOM_NOT_FOUND")
}

func TestGrain_TransferOwnership_RecordsTransfer(t *testing.T) {
	store := &fakeMembershipStore{loaded: aliceAndBob(t)}
	g := rolesGrain(t, store)

	resp, err := g.TransferOwnership(&roompb.TransferOwnershipRequest{
		Actor:          protoActor("1", "Alice"),
		NewOwnerUserId: "2",
	}, fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("expected success, got error: %+v", resp.GetError())
	}
	want := []transferCall{{actor: mustUserID(t, "1"), newOwner: mustUserID(t, "2")}}
	if !reflect.DeepEqual(store.transferCalls, want) {
		t.Errorf("RecordOwnershipTransfer calls: got %+v, want %+v", store.transferCalls, want)
	}
}

func TestGrain_TransferOwnership_Rejections(t *testing.T) {
	tests := []struct {
		name       string
		req        *roompb.TransferOwnershipRequest
		storeErr   error
		wantCode   int32
		wantStatus string
	}{
		{
			name:       "missing new owner",
			req:        &roompb.TransferOwnershipRequest{Actor: protoActor("1", "Alice")},
			wantCode:   4001,
			wantStatus: "INVALID_REQUEST",
		},
		{
			name:       "new owner not a member",
			req:        &roompb.TransferOwnershipRequest{Actor: protoActor("1", "Alice"), NewOwnerUserId: "9"},
			wantCode:   2001,
			wantStatus: "ROOM_NOT_MEMBER",
		},
		{
			name:       "non-owner actor maps to permission denied",
			req:        &roompb.TransferOwnershipRequest{Actor: protoActor("2", "Bob"), NewOwnerUserId: "1"},
			storeErr:   room.ErrRolePermissionDenied,
			wantCode:   2005,
			wantStatus: "ROOM_PERMISSION_DENIED",
		},
		{
			name:       "store failure maps to internal error",
			req:        &roompb.TransferOwnershipRequest{Actor: protoActor("1", "Alice"), NewOwnerUserId: "2"},
			storeErr:   errFake("db down"),
			wantCode:   5001,
			wantStatus: "INTERNAL_ERROR",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeMembershipStore{loaded: aliceAndBob(t), transferErr: tc.storeErr}
			g := rolesGrain(t, store)

			resp, err := g.TransferOwnership(tc.req, fakeRoomCtx(testRoomID))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertErrResponse(t, resp.GetError(), tc.wantCode, tc.wantStatus)
		})
	}
}

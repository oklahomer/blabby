package user_test

import (
	"errors"
	"reflect"
	"testing"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/id"
)

func TestGrain_SetRoomMemberRole(t *testing.T) {
	t.Run("forwards to the room with the grain's own identity as actor", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.SetRoomMemberRole(&userpb.SetRoomMemberRoleRequest{
			RoomId: "4", TargetUserId: "2", Role: "admin",
		}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}
		want := []setRoleCall{{RoomID: "4", Actor: userRef{ID: "1", Name: "1"}, Target: "2", Role: "admin"}}
		if !reflect.DeepEqual(h.rooms.setRoleCalls, want) {
			t.Errorf("SetMemberRole calls: got %+v, want %+v", h.rooms.setRoleCalls, want)
		}
	})

	t.Run("rejects a malformed room id before reaching the room", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.SetRoomMemberRole(&userpb.SetRoomMemberRoleRequest{
			RoomId: "not-a-room", TargetUserId: "2", Role: "admin",
		}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
		if len(h.rooms.setRoleCalls) != 0 {
			t.Errorf("room reached on a malformed room id: %+v", h.rooms.setRoleCalls)
		}
	})

	t.Run("translates a transport failure into INTERNAL_ERROR", func(t *testing.T) {
		h := newGrain(t)
		h.rooms.setRoleFn = func(id.RoomID, *roompb.SetMemberRoleRequest) (*roompb.SetMemberRoleResponse, error) {
			return nil, errors.New("dial tcp: connection refused")
		}

		resp, err := h.g.SetRoomMemberRole(&userpb.SetRoomMemberRoleRequest{
			RoomId: "4", TargetUserId: "2", Role: "admin",
		}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")
	})

	t.Run("relays the room's business error verbatim", func(t *testing.T) {
		h := newGrain(t)
		h.rooms.defaultSetRole = &roompb.SetMemberRoleResponse{
			Error: &commonpb.ErrorDetail{Code: 2005, Status: "ROOM_PERMISSION_DENIED", Message: "your role does not permit changing member roles"},
		}

		resp, err := h.g.SetRoomMemberRole(&userpb.SetRoomMemberRoleRequest{
			RoomId: "4", TargetUserId: "2", Role: "admin",
		}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2005, "ROOM_PERMISSION_DENIED")
	})
}

func TestGrain_TransferRoomOwnership(t *testing.T) {
	t.Run("forwards to the room with the grain's own identity as actor", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.TransferRoomOwnership(&userpb.TransferRoomOwnershipRequest{
			RoomId: "4", NewOwnerUserId: "2",
		}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}
		want := []transferOwnershipCall{{RoomID: "4", Actor: userRef{ID: "1", Name: "1"}, NewOwner: "2"}}
		if !reflect.DeepEqual(h.rooms.transferCalls, want) {
			t.Errorf("TransferOwnership calls: got %+v, want %+v", h.rooms.transferCalls, want)
		}
	})

	t.Run("rejects a malformed room id before reaching the room", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.TransferRoomOwnership(&userpb.TransferRoomOwnershipRequest{
			RoomId: "", NewOwnerUserId: "2",
		}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
		if len(h.rooms.transferCalls) != 0 {
			t.Errorf("room reached on a malformed room id: %+v", h.rooms.transferCalls)
		}
	})

	t.Run("relays the room's business error verbatim", func(t *testing.T) {
		h := newGrain(t)
		h.rooms.defaultTransfer = &roompb.TransferOwnershipResponse{
			Error: &commonpb.ErrorDetail{Code: 2001, Status: "ROOM_NOT_MEMBER", Message: "new owner is not a member of this room"},
		}

		resp, err := h.g.TransferRoomOwnership(&userpb.TransferRoomOwnershipRequest{
			RoomId: "4", NewOwnerUserId: "9",
		}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2001, "ROOM_NOT_MEMBER")
	})
}

package gateway

import (
	"context"
	"net/http"
	"testing"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/id"
)

// stubUserResolver resolves the seeded bare U-code bodies to internal ids,
// mirroring stubRoomDirectory's role for R… codes.
type stubUserResolver struct {
	byCode map[string]int64
}

func newStubUserResolver() *stubUserResolver {
	return &stubUserResolver{byCode: map[string]int64{
		"A000000001": 1,
		"B000000002": 2,
	}}
}

func (s *stubUserResolver) ResolveUserID(_ context.Context, code id.PublicCode) (id.UserID, error) {
	raw, ok := s.byCode[code.String()]
	if !ok {
		return id.UserID{}, auth.ErrPublicCodeUnknown
	}
	uid, err := id.NewUserID(raw)
	if err != nil {
		return id.UserID{}, err
	}
	return uid, nil
}

func grainDetail(code errcode.Code, msg string) *commonpb.ErrorDetail {
	return &commonpb.ErrorDetail{Code: code.Int32(), Status: code.Status(), Message: msg}
}

func TestHandleRoomMemberRolePut(t *testing.T) {
	const pattern = "PUT /rooms/{id}/members/{user}/role"
	const path = "/rooms/RG000000004/members/UB000000002/role"

	t.Run("forwards resolved ids and role to the user grain", func(t *testing.T) {
		fake := &fakeUserGrainCaller{setRoleResp: &userpb.SetRoomMemberRoleResponse{}}
		g := gatewayWithFake(fake)

		rec := servePath(t, g, http.MethodPut, pattern, path, `{"role":"admin"}`, "application/json", "1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		got := fake.setRoleReq
		if got.GetRoomId() != "4" || got.GetTargetUserId() != "2" || got.GetRole() != "admin" {
			t.Errorf("grain request = %+v, want room 4, target 2, role admin", got)
		}
	})

	tests := []struct {
		name          string
		path          string
		body          string
		contentType   string
		grain         *fakeUserGrainCaller
		wantStatus    int
		wantErrorCode errcode.Code
	}{
		{
			name: "malformed room code", path: "/rooms/nope/members/UB000000002/role",
			body: `{"role":"admin"}`, contentType: "application/json",
			wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest,
		},
		{
			name: "malformed user code", path: "/rooms/RG000000004/members/nope/role",
			body: `{"role":"admin"}`, contentType: "application/json",
			wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest,
		},
		{
			name: "unknown user code", path: "/rooms/RG000000004/members/UZ999999999/role",
			body: `{"role":"admin"}`, contentType: "application/json",
			wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest,
		},
		{
			name: "missing role", path: path,
			body: `{}`, contentType: "application/json",
			wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest,
		},
		{
			name: "wrong content type", path: path,
			body: `{"role":"admin"}`, contentType: "text/plain",
			wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest,
		},
		{
			name: "permission denied from the grain", path: path,
			body: `{"role":"admin"}`, contentType: "application/json",
			grain:      &fakeUserGrainCaller{setRoleResp: &userpb.SetRoomMemberRoleResponse{Error: grainDetail(errcode.RoomPermissionDenied, "your role does not permit changing member roles")}},
			wantStatus: http.StatusForbidden, wantErrorCode: errcode.RoomPermissionDenied,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := tc.grain
			if fake == nil {
				fake = &fakeUserGrainCaller{setRoleResp: &userpb.SetRoomMemberRoleResponse{}}
			}
			g := gatewayWithFake(fake)

			rec := servePath(t, g, http.MethodPut, pattern, tc.path, tc.body, tc.contentType, "1")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			resp := decodeErrorResponse(t, rec.Body)
			if resp.Error.Code != tc.wantErrorCode {
				t.Errorf("error.code: got %d, want %d", resp.Error.Code, tc.wantErrorCode)
			}
		})
	}
}

func TestHandleRoomOwnerPut(t *testing.T) {
	const pattern = "PUT /rooms/{id}/owner"
	const path = "/rooms/RG000000004/owner"

	t.Run("forwards the resolved new owner to the user grain", func(t *testing.T) {
		fake := &fakeUserGrainCaller{transferResp: &userpb.TransferRoomOwnershipResponse{}}
		g := gatewayWithFake(fake)

		rec := servePath(t, g, http.MethodPut, pattern, path, `{"user":"UB000000002"}`, "application/json", "1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		got := fake.transferReq
		if got.GetRoomId() != "4" || got.GetNewOwnerUserId() != "2" {
			t.Errorf("grain request = %+v, want room 4, new owner 2", got)
		}
	})

	tests := []struct {
		name          string
		body          string
		grain         *fakeUserGrainCaller
		wantStatus    int
		wantErrorCode errcode.Code
	}{
		{
			name: "missing user", body: `{}`,
			wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest,
		},
		{
			name: "unknown user", body: `{"user":"UZ999999999"}`,
			wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest,
		},
		{
			name: "permission denied is relayed", body: `{"user":"UB000000002"}`,
			grain:      &fakeUserGrainCaller{transferResp: &userpb.TransferRoomOwnershipResponse{Error: grainDetail(errcode.RoomPermissionDenied, "only the owner can transfer ownership")}},
			wantStatus: http.StatusForbidden, wantErrorCode: errcode.RoomPermissionDenied,
		},
		{
			name: "target not a member is relayed", body: `{"user":"UB000000002"}`,
			grain:      &fakeUserGrainCaller{transferResp: &userpb.TransferRoomOwnershipResponse{Error: grainDetail(errcode.RoomNotMember, "new owner is not a member of this room")}},
			wantStatus: http.StatusForbidden, wantErrorCode: errcode.RoomNotMember,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := tc.grain
			if fake == nil {
				fake = &fakeUserGrainCaller{transferResp: &userpb.TransferRoomOwnershipResponse{}}
			}
			g := gatewayWithFake(fake)

			rec := servePath(t, g, http.MethodPut, pattern, path, tc.body, "application/json", "1")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			resp := decodeErrorResponse(t, rec.Body)
			if resp.Error.Code != tc.wantErrorCode {
				t.Errorf("error.code: got %d, want %d", resp.Error.Code, tc.wantErrorCode)
			}
		})
	}
}

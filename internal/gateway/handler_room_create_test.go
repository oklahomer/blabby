package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/id"
)

// fakeRoomCreator records the creation call and returns a configured result.
type fakeRoomCreator struct {
	info     RoomInfo
	err      error
	called   bool
	gotActor id.UserID
	gotName  domain.RoomName
}

func (f *fakeRoomCreator) CreateRoom(_ context.Context, actor id.UserID, name domain.RoomName) (RoomInfo, error) {
	f.called = true
	f.gotActor = actor
	f.gotName = name
	return f.info, f.err
}

func createdInfo(t *testing.T) RoomInfo {
	t.Helper()
	rid, err := id.NewRoomID(42)
	if err != nil {
		t.Fatalf("NewRoomID: %v", err)
	}
	code, err := id.ParsePublicCode("K000000042")
	if err != nil {
		t.Fatalf("ParsePublicCode: %v", err)
	}
	return RoomInfo{ID: rid, Code: code, Name: "Standup"}
}

func gatewayForCreate(rc RoomCreator, fake userGrainCaller) *Gateway {
	return &Gateway{
		auth:        &stubAuthenticator{},
		rooms:       newStubRoomDirectory(),
		users:       newStubUserResolver(),
		roomCreator: rc,
		cluster:     sentinelCluster(),
		actorRoot:   sentinelActorRoot(),
		userGrain:   func(id.UserID) userGrainCaller { return fake },
	}
}

func TestHandleRoomCreate(t *testing.T) {
	const pattern = "POST /rooms"

	t.Run("creates the room and attempts joined-cache warm-up", func(t *testing.T) {
		creator := &fakeRoomCreator{info: createdInfo(t)}
		grain := &fakeUserGrainCaller{joinResp: &userpb.JoinRoomResponse{}}
		g := gatewayForCreate(creator, grain)

		rec := servePath(t, g, http.MethodPost, pattern, "/rooms", `{"name":"Standup"}`, "application/json", "1")
		if rec.Code != http.StatusCreated {
			t.Fatalf("status: got %d, want 201 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		decodeSuccess(t, rec.Body, &resp)
		if resp.ID != "RK000000042" || resp.Name != "Standup" {
			t.Errorf("response = %+v, want RK000000042 / Standup", resp)
		}
		if creator.gotActor.String() != "1" || creator.gotName.String() != "Standup" {
			t.Errorf("creator got actor=%s name=%q, want 1 / Standup", creator.gotActor, creator.gotName)
		}
		// The cache warm-up joins the freshly created room through the User grain.
		if grain.joinReq.GetRoomId() != "42" {
			t.Errorf("warm-up JoinRoom room id = %q, want 42", grain.joinReq.GetRoomId())
		}
	})

	t.Run("a failed warm-up does not fail the creation", func(t *testing.T) {
		creator := &fakeRoomCreator{info: createdInfo(t)}
		grain := &fakeUserGrainCaller{joinErr: errors.New("cluster unreachable")}
		g := gatewayForCreate(creator, grain)

		rec := servePath(t, g, http.MethodPost, pattern, "/rooms", `{"name":"Standup"}`, "application/json", "1")
		if rec.Code != http.StatusCreated {
			t.Fatalf("status: got %d, want 201 despite warm-up failure (body=%s)", rec.Code, rec.Body.String())
		}
	})

	tests := []struct {
		name          string
		body          string
		contentType   string
		creator       *fakeRoomCreator
		wantStatus    int
		wantErrorCode errcode.Code
		wantReached   bool
	}{
		{name: "blank name", body: `{"name":"   "}`, contentType: "application/json", wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "missing name", body: `{}`, contentType: "application/json", wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "name too long", body: `{"name":"` + strings.Repeat("a", domain.MaxRoomNameBytes+1) + `"}`, contentType: "application/json", wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "malformed body", body: `{"name":`, contentType: "application/json", wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{name: "wrong content type", body: `{"name":"Standup"}`, contentType: "text/plain", wantStatus: http.StatusBadRequest, wantErrorCode: errcode.InvalidRequest},
		{
			name: "service failure", body: `{"name":"Standup"}`, contentType: "application/json",
			creator:    &fakeRoomCreator{err: errors.New("db down")},
			wantStatus: http.StatusInternalServerError, wantErrorCode: errcode.InternalError, wantReached: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			creator := tc.creator
			if creator == nil {
				creator = &fakeRoomCreator{info: createdInfo(t)}
			}
			g := gatewayForCreate(creator, &fakeUserGrainCaller{joinResp: &userpb.JoinRoomResponse{}})

			rec := servePath(t, g, http.MethodPost, pattern, "/rooms", tc.body, tc.contentType, "1")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if creator.called != tc.wantReached {
				t.Errorf("creator reached = %v, want %v", creator.called, tc.wantReached)
			}
			resp := decodeErrorResponse(t, rec.Body)
			if resp.Error.Code != tc.wantErrorCode {
				t.Errorf("error.code: got %d, want %d", resp.Error.Code, tc.wantErrorCode)
			}
		})
	}
}

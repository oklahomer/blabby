package user_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/id"

	userpb "github.com/oklahomer/blabby/gen/user"
)

// stubJoinedRoomLoader is a configurable JoinedRoomLoader for activation tests.
type stubJoinedRoomLoader struct {
	rooms []domain.RoomRef
	err   error
}

func (s stubJoinedRoomLoader) ListJoinedRooms(context.Context, id.UserID) ([]domain.RoomRef, error) {
	return s.rooms, s.err
}

// joinedRoomRef builds an active RoomRef the loader can return.
func joinedRoomRef(t *testing.T, rawID, rawCode, name string) domain.RoomRef {
	t.Helper()
	rid, err := id.ParseRoomID(rawID)
	if err != nil {
		t.Fatalf("room id %s: %v", rawID, err)
	}
	code, err := id.ParsePublicCode(rawCode)
	if err != nil {
		t.Fatalf("public code %s: %v", rawCode, err)
	}
	return domain.RoomRef{ID: rid, PublicCode: code, Name: name, Status: domain.RoomStatusActive}
}

func TestGrain_Init_HydratesJoinedRooms(t *testing.T) {
	loader := stubJoinedRoomLoader{rooms: []domain.RoomRef{
		joinedRoomRef(t, "4", "G000000004", "General"),
		joinedRoomRef(t, "5", "H000000005", "Random"),
	}}
	g := &user.Grain{}
	g.SetRoomClient(&fakeRoomClient{})
	g.SetJoinedRoomLoader(loader)
	g.Init(fakeUserCtx("1"))

	want := []id.RoomID{mustRoomID(t, "4"), mustRoomID(t, "5")}
	if got := g.JoinedRooms(); !reflect.DeepEqual(got, want) {
		t.Errorf("JoinedRooms after activation: got %v, want %v", got, want)
	}

	// GetJoinedRooms serves the hydrated cache (no per-request DB read).
	resp, err := g.GetJoinedRooms(&userpb.GetJoinedRoomsRequest{}, fakeUserCtx("1"))
	if err != nil {
		t.Fatalf("GetJoinedRooms: %v", err)
	}
	gotCodes := make([]string, len(resp.GetRooms()))
	for i, r := range resp.GetRooms() {
		gotCodes[i] = r.GetPublicCode()
	}
	if want := []string{"G000000004", "H000000005"}; !reflect.DeepEqual(gotCodes, want) {
		t.Errorf("GetJoinedRooms codes: got %v, want %v", gotCodes, want)
	}
}

func TestGrain_Init_PanicsOnJoinedRoomsLoadError(t *testing.T) {
	g := &user.Grain{}
	g.SetRoomClient(&fakeRoomClient{})
	g.SetJoinedRoomLoader(stubJoinedRoomLoader{err: errors.New("db unreachable")})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Init: expected panic on a transient joined-rooms load error so the supervisor re-activates")
		}
	}()
	g.Init(fakeUserCtx("1"))
}

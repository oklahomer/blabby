package gateway

import (
	"errors"
	"strings"
	"testing"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/ids"
)

// Compile-time assertion: the per-user adapter satisfies the
// gateway-internal userGrainCaller seam.
var _ userGrainCaller = (*userGrainClient)(nil)

// fakeUserGrainCaller records the most recent call and returns canned
// responses. It satisfies userGrainCaller for handler tests.
type fakeUserGrainCaller struct {
	joinReq       *userpb.JoinRoomRequest
	joinResp      *userpb.JoinRoomResponse
	joinErr       error
	leaveReq      *userpb.LeaveRoomRequest
	leaveResp     *userpb.LeaveRoomResponse
	leaveErr      error
	sendReq       *userpb.SendMessageRequest
	sendResp      *userpb.SendMessageResponse
	sendErr       error
	getJoinedReq  *userpb.GetJoinedRoomsRequest
	getJoinedResp *userpb.GetJoinedRoomsResponse
	getJoinedErr  error
	calls         int
}

func (f *fakeUserGrainCaller) JoinRoom(req *userpb.JoinRoomRequest) (*userpb.JoinRoomResponse, error) {
	f.calls++
	f.joinReq = req
	return f.joinResp, f.joinErr
}

func (f *fakeUserGrainCaller) LeaveRoom(req *userpb.LeaveRoomRequest) (*userpb.LeaveRoomResponse, error) {
	f.calls++
	f.leaveReq = req
	return f.leaveResp, f.leaveErr
}

func (f *fakeUserGrainCaller) SendMessage(req *userpb.SendMessageRequest) (*userpb.SendMessageResponse, error) {
	f.calls++
	f.sendReq = req
	return f.sendResp, f.sendErr
}

func (f *fakeUserGrainCaller) GetJoinedRooms(req *userpb.GetJoinedRoomsRequest) (*userpb.GetJoinedRoomsResponse, error) {
	f.calls++
	f.getJoinedReq = req
	return f.getJoinedResp, f.getJoinedErr
}

func TestUserGrainFor_FallsThroughToClusterAdapter_WhenSeamUnset(t *testing.T) {
	g := &Gateway{} // userGrain seam unset, cluster nil

	// The production fall-through reaches the generated cluster client
	// constructor, which panics on a nil cluster. Asserting that panic
	// proves three things at once: the userGrain seam was nil, the
	// fall-through ran, and the panic is the expected guard rather than
	// an unrelated crash.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected nil-cluster panic from production fall-through; got no panic")
		}
		err, ok := r.(error)
		if !ok || !strings.Contains(err.Error(), "nil cluster") {
			t.Errorf("expected nil-cluster panic from GetUserGrainGrainClient, got %v", r)
		}
	}()
	g.userGrainFor(mustUserID(t, "user-1"))
}

func TestUserGrainFor_UsesTestSeamWhenSet(t *testing.T) {
	fake := &fakeUserGrainCaller{joinErr: errors.New("boom")}
	g := &Gateway{userGrain: func(userID ids.UserID) userGrainCaller {
		if userID.String() != "user-1" {
			t.Fatalf("userGrainFor passed unexpected userID: got %q, want %q", userID.String(), "user-1")
		}
		return fake
	}}

	got := g.userGrainFor(mustUserID(t, "user-1"))
	if got != fake {
		t.Fatalf("userGrainFor: got %v, want injected fake %v", got, fake)
	}

	if _, err := got.JoinRoom(&userpb.JoinRoomRequest{RoomId: "general"}); err == nil {
		t.Fatal("expected JoinRoom error from fake, got nil")
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 call to fake, got %d", fake.calls)
	}
}

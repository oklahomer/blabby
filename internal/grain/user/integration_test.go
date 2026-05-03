package user_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/cluster"

	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
)

// stubRoomGrain is a minimal RoomGrain implementation used to exercise the
// clusterRoomClient production path end-to-end without triggering Room
// grain's fan-out (which would call back into the same UserGrain and
// deadlock the request the test is awaiting).
type stubRoomGrain struct {
	joinCount    *int64
	leaveCount   *int64
	postCount    *int64
	postResponse *roompb.PostMessageResponse
}

func (s *stubRoomGrain) Init(cluster.GrainContext)           {}
func (s *stubRoomGrain) Terminate(cluster.GrainContext)      {}
func (s *stubRoomGrain) ReceiveDefault(cluster.GrainContext) {}

func (s *stubRoomGrain) Join(*roompb.JoinRequest, cluster.GrainContext) (*roompb.JoinResponse, error) {
	atomic.AddInt64(s.joinCount, 1)
	return &roompb.JoinResponse{Success: true}, nil
}
func (s *stubRoomGrain) Leave(*roompb.LeaveRequest, cluster.GrainContext) (*roompb.LeaveResponse, error) {
	atomic.AddInt64(s.leaveCount, 1)
	return &roompb.LeaveResponse{Success: true}, nil
}
func (s *stubRoomGrain) PostMessage(*roompb.PostMessageRequest, cluster.GrainContext) (*roompb.PostMessageResponse, error) {
	atomic.AddInt64(s.postCount, 1)
	if s.postResponse != nil {
		return s.postResponse, nil
	}
	return &roompb.PostMessageResponse{Success: true, Timestamp: time.Now().UnixMilli()}, nil
}

// TestUserGrain_Integration_RoutesCommandsThroughCluster drives the full
// User grain command surface through the generated cluster client. Uses
// the package-shared cluster (see TestMain in main_test.go); a unique user
// identity ("alice-integration") keeps this test isolated from sibling
// tests that share the same cluster.
//
// Covers NewKind, Init's clusterRoomClient wiring, and the
// clusterRoomClient.Join/Leave/PostMessage dispatch path that unit tests
// against the fake roomClient cannot reach.
func TestUserGrain_Integration_RoutesCommandsThroughCluster(t *testing.T) {
	resetStubRoomCounters()
	const userID = "alice-integration"
	uc := userpb.GetUserGrainGrainClient(sharedCluster, userID)

	// JoinRoom — exercises clusterRoomClient.Join end-to-end.
	joinResp, err := uc.JoinRoom(&userpb.JoinRoomRequest{RoomId: "general"})
	if err != nil {
		t.Fatalf("JoinRoom via cluster: %v", err)
	}
	if !joinResp.GetSuccess() {
		ed := joinResp.GetError()
		t.Fatalf("JoinRoom: success=false code=%d status=%q msg=%q",
			ed.GetCode(), ed.GetStatus(), ed.GetMessage())
	}
	if got := atomic.LoadInt64(&stubRoomJoinCount); got != 1 {
		t.Errorf("stub RoomGrain.Join calls: got %d, want 1", got)
	}

	// GetJoinedRooms — verifies the User grain recorded the membership.
	listResp, err := uc.GetJoinedRooms(&userpb.GetJoinedRoomsRequest{})
	if err != nil {
		t.Fatalf("GetJoinedRooms via cluster: %v", err)
	}
	if got := listResp.GetRoomIds(); len(got) != 1 || got[0] != "general" {
		t.Errorf("RoomIds: got %v, want [general]", got)
	}

	// SendMessage — exercises clusterRoomClient.PostMessage and the
	// timestamp pass-through.
	sendResp, err := uc.SendMessage(&userpb.SendMessageRequest{RoomId: "general", Text: "integration"})
	if err != nil {
		t.Fatalf("SendMessage via cluster: %v", err)
	}
	if !sendResp.GetSuccess() {
		ed := sendResp.GetError()
		t.Fatalf("SendMessage: success=false code=%d status=%q msg=%q",
			ed.GetCode(), ed.GetStatus(), ed.GetMessage())
	}
	if sendResp.GetTimestamp() != stubPostTimestamp {
		t.Errorf("Timestamp: got %d, want %d (stub response)", sendResp.GetTimestamp(), stubPostTimestamp)
	}
	if got := atomic.LoadInt64(&stubRoomPostCount); got != 1 {
		t.Errorf("stub RoomGrain.PostMessage calls: got %d, want 1", got)
	}

	// LeaveRoom — exercises clusterRoomClient.Leave.
	leaveResp, err := uc.LeaveRoom(&userpb.LeaveRoomRequest{RoomId: "general"})
	if err != nil {
		t.Fatalf("LeaveRoom via cluster: %v", err)
	}
	if !leaveResp.GetSuccess() {
		ed := leaveResp.GetError()
		t.Fatalf("LeaveRoom: success=false code=%d status=%q msg=%q",
			ed.GetCode(), ed.GetStatus(), ed.GetMessage())
	}
	if got := atomic.LoadInt64(&stubRoomLeaveCount); got != 1 {
		t.Errorf("stub RoomGrain.Leave calls: got %d, want 1", got)
	}

	// Connection registration round-trip — uses synthetic PID values; the
	// per-connection delivery path and Watch-driven eviction are exercised
	// end-to-end in sender_pid_test.go with real receiver actors.
	regResp, err := uc.RegisterConnection(&userpb.RegisterConnectionRequest{
		RequesterPid: &userpb.PID{
			Address: "test-addr",
			Id:      "test-pid",
		},
	})
	if err != nil {
		t.Fatalf("RegisterConnection via cluster: %v", err)
	}
	if !regResp.GetSuccess() {
		t.Fatalf("RegisterConnection: success=false code=%d", regResp.GetError().GetCode())
	}

	fwdResp, err := uc.ForwardMessage(&userpb.ForwardMessageRequest{
		RoomId: "general", SenderId: userID, Text: "hi", Timestamp: 1,
	})
	if err != nil {
		t.Fatalf("ForwardMessage via cluster: %v", err)
	}
	if !fwdResp.GetSuccess() {
		t.Errorf("ForwardMessage: success=false")
	}

	notifyResp, err := uc.NotifyRoomEvent(&userpb.NotifyRoomEventRequest{
		RoomId: "general", UserId: "bob", EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED,
	})
	if err != nil {
		t.Fatalf("NotifyRoomEvent via cluster: %v", err)
	}
	if !notifyResp.GetSuccess() {
		t.Errorf("NotifyRoomEvent: success=false")
	}
}

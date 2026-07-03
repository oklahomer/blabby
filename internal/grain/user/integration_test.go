package user_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
)

// stubRoomGrain is a minimal RoomGrain implementation used to exercise the
// clusterRoomClient production path end-to-end in isolation from the Room
// grain's member fan-out, so this test asserts command routing and call
// counts without fan-out interleaving. (Real Room→User fan-out, including the
// acting user's self-echo, is covered by the room package's fan-out
// integration test.)
type stubRoomGrain struct {
	joinCount    *int64
	leaveCount   *int64
	postCount    *int64
	postResponse *roompb.PostMessageResponse

	// joinUserName / postUserName capture the most recent User.Name the
	// stub observed on Join/PostMessage so the integration test can prove
	// the directory-seeded display name reaches the Room grain unchanged.
	joinUserName *atomic.Pointer[string]
	postUserName *atomic.Pointer[string]
}

func (s *stubRoomGrain) Init(cluster.GrainContext)           {}
func (s *stubRoomGrain) Terminate(cluster.GrainContext)      {}
func (s *stubRoomGrain) ReceiveDefault(cluster.GrainContext) {}

func (s *stubRoomGrain) Join(req *roompb.JoinRequest, _ cluster.GrainContext) (*roompb.JoinResponse, error) {
	atomic.AddInt64(s.joinCount, 1)
	name := req.GetUser().GetName()
	s.joinUserName.Store(&name)
	// A loaded Room grain returns its RoomRef so the User grain can cache it.
	return &roompb.JoinResponse{Room: &commonpb.RoomRef{
		RoomId:     "4",
		PublicCode: "G000000004",
		Name:       "General",
		Status:     "active",
	}}, nil
}
func (s *stubRoomGrain) Leave(*roompb.LeaveRequest, cluster.GrainContext) (*roompb.LeaveResponse, error) {
	atomic.AddInt64(s.leaveCount, 1)
	return &roompb.LeaveResponse{}, nil
}
func (s *stubRoomGrain) PostMessage(req *roompb.PostMessageRequest, _ cluster.GrainContext) (*roompb.PostMessageResponse, error) {
	atomic.AddInt64(s.postCount, 1)
	name := req.GetUser().GetName()
	s.postUserName.Store(&name)
	if s.postResponse != nil {
		return s.postResponse, nil
	}
	return &roompb.PostMessageResponse{Timestamp: timestamppb.Now()}, nil
}
func (s *stubRoomGrain) SetMemberRole(*roompb.SetMemberRoleRequest, cluster.GrainContext) (*roompb.SetMemberRoleResponse, error) {
	return &roompb.SetMemberRoleResponse{}, nil
}
func (s *stubRoomGrain) TransferOwnership(*roompb.TransferOwnershipRequest, cluster.GrainContext) (*roompb.TransferOwnershipResponse, error) {
	return &roompb.TransferOwnershipResponse{}, nil
}

// TestUserGrain_Integration_RoutesCommandsThroughCluster drives the full
// User grain command surface through the generated cluster client. Uses
// the package-shared cluster (see TestMain in main_test.go); a unique user
// identity ("11") keeps this test isolated from sibling
// tests that share the same cluster.
//
// Covers NewKind, Init's clusterRoomClient wiring, and the
// clusterRoomClient.Join/Leave/PostMessage dispatch path that unit tests
// against the fake roomClient cannot reach.
func TestUserGrain_Integration_RoutesCommandsThroughCluster(t *testing.T) {
	resetStubRoomCounters()
	const userID = "11"
	uc := userpb.GetUserGrainGrainClient(sharedCluster, userID)

	// JoinRoom — exercises clusterRoomClient.Join end-to-end.
	joinResp, err := uc.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"})
	if err != nil {
		t.Fatalf("JoinRoom via cluster: %v", err)
	}
	if ed := joinResp.GetError(); ed != nil {
		t.Fatalf("JoinRoom: error code=%d status=%q msg=%q",
			ed.GetCode(), ed.GetStatus(), ed.GetMessage())
	}
	if got := atomic.LoadInt64(&stubRoomJoinCount); got != 1 {
		t.Errorf("stub RoomGrain.Join calls: got %d, want 1", got)
	}
	// The display name seeded by the fake directory (see main_test.go) must
	// ride the JoinRequest.User ref all the way into the Room grain.
	if got := stubRoomJoinUserName.Load(); got == nil || *got != seededDisplayName {
		t.Errorf("Join UserRef name: got %v, want %q", got, seededDisplayName)
	}

	// GetJoinedRooms — verifies the User grain recorded the membership.
	listResp, err := uc.GetJoinedRooms(&userpb.GetJoinedRoomsRequest{})
	if err != nil {
		t.Fatalf("GetJoinedRooms via cluster: %v", err)
	}
	if rooms := listResp.GetRooms(); len(rooms) != 1 || rooms[0].GetRoomId() != "4" || rooms[0].GetName() != "General" {
		t.Errorf("Rooms: got %v, want one ref for room 4 named General", rooms)
	}

	// SendMessage — exercises clusterRoomClient.PostMessage and the
	// timestamp pass-through.
	sendResp, err := uc.SendMessage(&userpb.SendMessageRequest{RoomId: "4", Text: "integration"})
	if err != nil {
		t.Fatalf("SendMessage via cluster: %v", err)
	}
	if ed := sendResp.GetError(); ed != nil {
		t.Fatalf("SendMessage: error code=%d status=%q msg=%q",
			ed.GetCode(), ed.GetStatus(), ed.GetMessage())
	}
	if got := sendResp.GetTimestamp(); got == nil || !got.AsTime().Equal(stubPostTimestamp) {
		t.Errorf("Timestamp: got %v, want %v (stub response)", got, stubPostTimestamp)
	}
	if got := atomic.LoadInt64(&stubRoomPostCount); got != 1 {
		t.Errorf("stub RoomGrain.PostMessage calls: got %d, want 1", got)
	}
	// Same seeded name must ride the PostMessageRequest.User ref too.
	if got := stubRoomPostUserName.Load(); got == nil || *got != seededDisplayName {
		t.Errorf("PostMessage UserRef name: got %v, want %q", got, seededDisplayName)
	}

	// LeaveRoom — exercises clusterRoomClient.Leave.
	leaveResp, err := uc.LeaveRoom(&userpb.LeaveRoomRequest{RoomId: "4"})
	if err != nil {
		t.Fatalf("LeaveRoom via cluster: %v", err)
	}
	if ed := leaveResp.GetError(); ed != nil {
		t.Fatalf("LeaveRoom: error code=%d status=%q msg=%q",
			ed.GetCode(), ed.GetStatus(), ed.GetMessage())
	}
	if got := atomic.LoadInt64(&stubRoomLeaveCount); got != 1 {
		t.Errorf("stub RoomGrain.Leave calls: got %d, want 1", got)
	}

	// Connection registration round-trip. Use a local no-op actor so the shared
	// cluster does not retain a retrying endpoint writer for a synthetic remote
	// address during TestMain shutdown. Delivery and Watch-driven eviction are
	// exercised end-to-end in sender_pid_test.go.
	connectionPID := sharedCluster.ActorSystem.Root.Spawn(actor.PropsFromFunc(func(actor.Context) {}))
	t.Cleanup(func() { _ = sharedCluster.ActorSystem.Root.PoisonFuture(connectionPID).Wait() })
	regResp, err := uc.RegisterConnection(&userpb.RegisterConnectionRequest{
		RequesterPid: &userpb.PID{
			Address: connectionPID.GetAddress(),
			Id:      connectionPID.GetId(),
		},
	})
	if err != nil {
		t.Fatalf("RegisterConnection via cluster: %v", err)
	}
	if ed := regResp.GetError(); ed != nil {
		t.Fatalf("RegisterConnection: error code=%d", ed.GetCode())
	}

	room := &commonpb.RoomRef{RoomId: "4", PublicCode: "G000000004", Name: "General", Status: "active"}
	if _, err := uc.ForwardMessage(&userpb.ForwardMessageRequest{
		Room: room, Sender: &commonpb.UserRef{Id: userID, Name: seededDisplayName}, Text: "hi", Timestamp: timestamppb.New(time.UnixMilli(1)),
	}); err != nil {
		t.Fatalf("ForwardMessage via cluster: %v", err)
	}

	if _, err := uc.NotifyRoomEvent(&userpb.NotifyRoomEventRequest{
		Room: room, User: &commonpb.UserRef{Id: "2", Name: "Bob Example"}, EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED,
	}); err != nil {
		t.Fatalf("NotifyRoomEvent via cluster: %v", err)
	}
}

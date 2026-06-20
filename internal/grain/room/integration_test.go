package room_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/cluster"

	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/grain/room"
	clustertest "github.com/oklahomer/blabby/internal/testutil/cluster"
	graintest "github.com/oklahomer/blabby/internal/testutil/grain"
)

// stubUserGrain is a minimal UserGrain implementation used to exercise the
// clusterUserNotifier production path end-to-end. It records every
// NotifyRoomEvent / ForwardMessage call so the test can assert that fan-out
// from Room grain actually traverses the cluster.
type stubUserGrain struct {
	notifyCount  *int64
	forwardCount *int64
}

func (s *stubUserGrain) Init(cluster.GrainContext)           {}
func (s *stubUserGrain) Terminate(cluster.GrainContext)      {}
func (s *stubUserGrain) ReceiveDefault(cluster.GrainContext) {}

func (s *stubUserGrain) RegisterConnection(*userpb.RegisterConnectionRequest, cluster.GrainContext) (*userpb.RegisterConnectionResponse, error) {
	return &userpb.RegisterConnectionResponse{}, nil
}
func (s *stubUserGrain) JoinRoom(*userpb.JoinRoomRequest, cluster.GrainContext) (*userpb.JoinRoomResponse, error) {
	return &userpb.JoinRoomResponse{}, nil
}
func (s *stubUserGrain) LeaveRoom(*userpb.LeaveRoomRequest, cluster.GrainContext) (*userpb.LeaveRoomResponse, error) {
	return &userpb.LeaveRoomResponse{}, nil
}
func (s *stubUserGrain) SendMessage(*userpb.SendMessageRequest, cluster.GrainContext) (*userpb.SendMessageResponse, error) {
	return &userpb.SendMessageResponse{}, nil
}
func (s *stubUserGrain) ForwardMessage(*userpb.ForwardMessageRequest, cluster.GrainContext) (*userpb.ForwardMessageResponse, error) {
	atomic.AddInt64(s.forwardCount, 1)
	return &userpb.ForwardMessageResponse{}, nil
}
func (s *stubUserGrain) NotifyRoomEvent(*userpb.NotifyRoomEventRequest, cluster.GrainContext) (*userpb.NotifyRoomEventResponse, error) {
	atomic.AddInt64(s.notifyCount, 1)
	return &userpb.NotifyRoomEventResponse{}, nil
}
func (s *stubUserGrain) GetJoinedRooms(*userpb.GetJoinedRoomsRequest, cluster.GrainContext) (*userpb.GetJoinedRoomsResponse, error) {
	return &userpb.GetJoinedRoomsResponse{}, nil
}

// TestRoomGrain_Integration brings up an in-process cluster, registers Room
// grain (production constructor) and a stub User grain, then drives Join
// and PostMessage through the generated cluster client. This is the single
// integration smoke test mandated by the story; it covers NewKind, Init's
// notifier wiring, and the clusterUserNotifier dispatch path that unit tests
// cannot reach.
func TestRoomGrain_Integration_FanOutThroughCluster(t *testing.T) {
	var notifyCount, forwardCount int64

	userKind := userpb.NewUserGrainKind(func() userpb.UserGrain {
		return &stubUserGrain{
			notifyCount:  &notifyCount,
			forwardCount: &forwardCount,
		}
	}, time.Minute)
	roomKind := room.NewKind()

	c := clustertest.Start(t, roomKind, userKind)

	// Single-member topology settles within the cluster request timeout
	// (configured in clustertest.Start). The Request calls below will
	// retry internally until the topology is ready.
	roomClient := roompb.GetRoomGrainGrainClient(c, "general")

	joinResp, err := roomClient.Join(graintest.NewJoinRequest("1"))
	if err != nil {
		t.Fatalf("Join via cluster: %v", err)
	}
	if joinResp.GetError() != nil {
		t.Fatalf("Join: error=%+v", joinResp.GetError())
	}

	postResp, err := roomClient.PostMessage(graintest.NewPostMessageRequest("1", "integration"))
	if err != nil {
		t.Fatalf("PostMessage via cluster: %v", err)
	}
	if postResp.GetError() != nil {
		t.Fatalf("PostMessage: error=%+v", postResp.GetError())
	}
	if ts := postResp.GetTimestamp(); ts == nil || ts.AsTime().IsZero() {
		t.Errorf("Timestamp: got %v, want non-zero", ts)
	}

	// Allow async fan-out to settle.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&notifyCount) >= 1 && atomic.LoadInt64(&forwardCount) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := atomic.LoadInt64(&notifyCount); got < 1 {
		t.Errorf("stub UserGrain.NotifyRoomEvent calls: got %d, want >= 1 (Join fan-out)", got)
	}
	if got := atomic.LoadInt64(&forwardCount); got < 1 {
		t.Errorf("stub UserGrain.ForwardMessage calls: got %d, want >= 1 (PostMessage fan-out)", got)
	}
}

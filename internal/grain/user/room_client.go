package user

import (
	"fmt"

	"github.com/asynkron/protoactor-go/cluster"

	roompb "github.com/oklahomer/blabby/gen/room"
)

// roomClient abstracts calls into Room grains so the User grain can be
// unit-tested without a real cluster. Mirrors the userNotifier seam in
// internal/grain/room (Story 3.1) — small interface, recording fake in
// tests, thin cluster wrapper in production.
type roomClient interface {
	Join(roomID string, req *roompb.JoinRequest) (*roompb.JoinResponse, error)
	Leave(roomID string, req *roompb.LeaveRequest) (*roompb.LeaveResponse, error)
	PostMessage(roomID string, req *roompb.PostMessageRequest) (*roompb.PostMessageResponse, error)
}

// clusterRoomClient is the production roomClient; it routes calls to the
// real Room grain via the generated grain client.
type clusterRoomClient struct {
	c *cluster.Cluster
}

// newClusterRoomClient constructs the production roomClient bound to c.
// Panics on nil c so the failure surfaces at construction time rather than
// at the first roompb.GetRoomGrainGrainClient call several methods later.
func newClusterRoomClient(c *cluster.Cluster) *clusterRoomClient {
	if c == nil {
		panic("user grain: newClusterRoomClient called with nil cluster")
	}
	return &clusterRoomClient{c: c}
}

func (r *clusterRoomClient) Join(roomID string, req *roompb.JoinRequest) (*roompb.JoinResponse, error) {
	resp, err := roompb.GetRoomGrainGrainClient(r.c, roomID).Join(req)
	if err != nil {
		return nil, fmt.Errorf("room grain Join: %w", err)
	}
	return resp, nil
}

func (r *clusterRoomClient) Leave(roomID string, req *roompb.LeaveRequest) (*roompb.LeaveResponse, error) {
	resp, err := roompb.GetRoomGrainGrainClient(r.c, roomID).Leave(req)
	if err != nil {
		return nil, fmt.Errorf("room grain Leave: %w", err)
	}
	return resp, nil
}

func (r *clusterRoomClient) PostMessage(roomID string, req *roompb.PostMessageRequest) (*roompb.PostMessageResponse, error) {
	resp, err := roompb.GetRoomGrainGrainClient(r.c, roomID).PostMessage(req)
	if err != nil {
		return nil, fmt.Errorf("room grain PostMessage: %w", err)
	}
	return resp, nil
}

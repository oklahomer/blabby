package user

import (
	"fmt"

	"github.com/asynkron/protoactor-go/cluster"

	roompb "github.com/oklahomer/blabby/gen/room"
	"github.com/oklahomer/blabby/internal/id"
)

// roomClient abstracts calls into Room grains so the User grain can be
// unit-tested without a real cluster. Mirrors the userNotifier seam in
// internal/grain/room — small interface, recording fake in tests, thin
// cluster wrapper in production.
type roomClient interface {
	Join(roomID id.RoomID, req *roompb.JoinRequest) (*roompb.JoinResponse, error)
	Leave(roomID id.RoomID, req *roompb.LeaveRequest) (*roompb.LeaveResponse, error)
	PostMessage(roomID id.RoomID, req *roompb.PostMessageRequest) (*roompb.PostMessageResponse, error)
	SetMemberRole(roomID id.RoomID, req *roompb.SetMemberRoleRequest) (*roompb.SetMemberRoleResponse, error)
	TransferOwnership(roomID id.RoomID, req *roompb.TransferOwnershipRequest) (*roompb.TransferOwnershipResponse, error)
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

func (r *clusterRoomClient) Join(roomID id.RoomID, req *roompb.JoinRequest) (*roompb.JoinResponse, error) {
	resp, err := roompb.GetRoomGrainGrainClient(r.c, roomID.String()).Join(req)
	if err != nil {
		return nil, fmt.Errorf("room grain Join: %w", err)
	}
	return resp, nil
}

func (r *clusterRoomClient) Leave(roomID id.RoomID, req *roompb.LeaveRequest) (*roompb.LeaveResponse, error) {
	resp, err := roompb.GetRoomGrainGrainClient(r.c, roomID.String()).Leave(req)
	if err != nil {
		return nil, fmt.Errorf("room grain Leave: %w", err)
	}
	return resp, nil
}

func (r *clusterRoomClient) PostMessage(roomID id.RoomID, req *roompb.PostMessageRequest) (*roompb.PostMessageResponse, error) {
	resp, err := roompb.GetRoomGrainGrainClient(r.c, roomID.String()).PostMessage(req)
	if err != nil {
		return nil, fmt.Errorf("room grain PostMessage: %w", err)
	}
	return resp, nil
}

func (r *clusterRoomClient) SetMemberRole(roomID id.RoomID, req *roompb.SetMemberRoleRequest) (*roompb.SetMemberRoleResponse, error) {
	resp, err := roompb.GetRoomGrainGrainClient(r.c, roomID.String()).SetMemberRole(req)
	if err != nil {
		return nil, fmt.Errorf("room grain SetMemberRole: %w", err)
	}
	return resp, nil
}

func (r *clusterRoomClient) TransferOwnership(roomID id.RoomID, req *roompb.TransferOwnershipRequest) (*roompb.TransferOwnershipResponse, error) {
	resp, err := roompb.GetRoomGrainGrainClient(r.c, roomID.String()).TransferOwnership(req)
	if err != nil {
		return nil, fmt.Errorf("room grain TransferOwnership: %w", err)
	}
	return resp, nil
}

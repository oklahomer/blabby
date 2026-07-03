package gateway

import (
	"github.com/asynkron/protoactor-go/cluster"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/id"
)

// userGrainCaller abstracts the four User-grain RPCs that this package
// dispatches from HTTP handlers. The seam exists so handler tests can
// inject a fake without spinning up a cluster. Production callers obtain
// one via newClusterUserGrainCaller(g.cluster).callerFor(userID).
type userGrainCaller interface {
	JoinRoom(req *userpb.JoinRoomRequest) (*userpb.JoinRoomResponse, error)
	LeaveRoom(req *userpb.LeaveRoomRequest) (*userpb.LeaveRoomResponse, error)
	SendMessage(req *userpb.SendMessageRequest) (*userpb.SendMessageResponse, error)
	GetJoinedRooms(req *userpb.GetJoinedRoomsRequest) (*userpb.GetJoinedRoomsResponse, error)
	SetRoomMemberRole(req *userpb.SetRoomMemberRoleRequest) (*userpb.SetRoomMemberRoleResponse, error)
	TransferRoomOwnership(req *userpb.TransferRoomOwnershipRequest) (*userpb.TransferRoomOwnershipResponse, error)
}

// clusterUserGrainCaller wraps the generated cluster client to satisfy
// userGrainCaller. The wrapper is per-user: callers obtain one via
// newClusterUserGrainCaller(c).callerFor(userID).
type clusterUserGrainCaller struct {
	cluster *cluster.Cluster
}

func newClusterUserGrainCaller(c *cluster.Cluster) *clusterUserGrainCaller {
	return &clusterUserGrainCaller{cluster: c}
}

// callerFor returns a per-user userGrainCaller backed by the generated
// cluster client. The returned caller drops the variadic
// cluster.GrainCallOption tail because the gateway has no use for it
// today; if a future story needs per-call options, widen the interface
// before reaching for this seam.
func (c *clusterUserGrainCaller) callerFor(userID id.UserID) userGrainCaller {
	return &userGrainClient{client: userpb.GetUserGrainGrainClient(c.cluster, userID.String())}
}

// userGrainClient adapts the generated *userpb.UserGrainGrainClient to the
// fixed-arity userGrainCaller interface by dropping the variadic
// cluster.GrainCallOption tail at each call site.
type userGrainClient struct {
	client *userpb.UserGrainGrainClient
}

func (u *userGrainClient) JoinRoom(req *userpb.JoinRoomRequest) (*userpb.JoinRoomResponse, error) {
	return u.client.JoinRoom(req)
}

func (u *userGrainClient) LeaveRoom(req *userpb.LeaveRoomRequest) (*userpb.LeaveRoomResponse, error) {
	return u.client.LeaveRoom(req)
}

func (u *userGrainClient) SendMessage(req *userpb.SendMessageRequest) (*userpb.SendMessageResponse, error) {
	return u.client.SendMessage(req)
}

func (u *userGrainClient) GetJoinedRooms(req *userpb.GetJoinedRoomsRequest) (*userpb.GetJoinedRoomsResponse, error) {
	return u.client.GetJoinedRooms(req)
}

func (u *userGrainClient) SetRoomMemberRole(req *userpb.SetRoomMemberRoleRequest) (*userpb.SetRoomMemberRoleResponse, error) {
	return u.client.SetRoomMemberRole(req)
}

func (u *userGrainClient) TransferRoomOwnership(req *userpb.TransferRoomOwnershipRequest) (*userpb.TransferRoomOwnershipResponse, error) {
	return u.client.TransferRoomOwnership(req)
}

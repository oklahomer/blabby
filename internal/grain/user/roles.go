package user

import (
	"log/slog"

	"github.com/asynkron/protoactor-go/cluster"

	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/id"
)

// Event-name constants for the role-management command routing, following the
// same convention as the join/leave events in user.go.
const (
	eventUserRoleChangeRejected        = "user.role.change_rejected"
	eventUserOwnershipTransferRejected = "user.ownership.transfer_rejected"
)

// SetRoomMemberRole routes a role change to the Room grain, attaching this
// grain's server-resolved identity as the acting member. The Room grain owns
// every role rule (membership, policy, the target's existence); this grain
// validates only the room id it consumes for routing, mutates no local state,
// and relays the room's verdict.
func (g *Grain) SetRoomMemberRole(req *userpb.SetRoomMemberRoleRequest, ctx cluster.GrainContext) (*userpb.SetRoomMemberRoleResponse, error) {
	roomID, err := id.ParseRoomID(req.GetRoomId())
	if err != nil {
		slog.Warn(eventUserRoleChangeRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", ctx.Identity(),
			"room_id", req.GetRoomId(),
			"reason", errcode.InvalidRequest.Status(),
		)
		return &userpb.SetRoomMemberRoleResponse{Error: errDetail(errcode.InvalidRequest, "room_id is required")}, nil
	}

	roomResp, err := g.rooms.SetMemberRole(roomID, &roompb.SetMemberRoleRequest{
		Actor:        g.self,
		TargetUserId: req.GetTargetUserId(),
		Role:         req.GetRole(),
	})
	if err != nil {
		logTransportError(ctx, "SetRoomMemberRole", roomID, err)
		return &userpb.SetRoomMemberRoleResponse{Error: errDetail(errcode.InternalError, "failed to reach room")}, nil
	}
	if roomErr := roomResp.GetError(); roomErr != nil {
		_, detail := parseRoomError(ctx, "SetRoomMemberRole", roomID, roomErr)
		return &userpb.SetRoomMemberRoleResponse{Error: detail}, nil
	}
	return &userpb.SetRoomMemberRoleResponse{}, nil
}

// TransferRoomOwnership routes an ownership transfer to the Room grain,
// attaching this grain's server-resolved identity as the current owner. Like
// SetRoomMemberRole it validates only the room id and relays the room's verdict.
func (g *Grain) TransferRoomOwnership(req *userpb.TransferRoomOwnershipRequest, ctx cluster.GrainContext) (*userpb.TransferRoomOwnershipResponse, error) {
	roomID, err := id.ParseRoomID(req.GetRoomId())
	if err != nil {
		slog.Warn(eventUserOwnershipTransferRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", ctx.Identity(),
			"room_id", req.GetRoomId(),
			"reason", errcode.InvalidRequest.Status(),
		)
		return &userpb.TransferRoomOwnershipResponse{Error: errDetail(errcode.InvalidRequest, "room_id is required")}, nil
	}

	roomResp, err := g.rooms.TransferOwnership(roomID, &roompb.TransferOwnershipRequest{
		Actor:          g.self,
		NewOwnerUserId: req.GetNewOwnerUserId(),
	})
	if err != nil {
		logTransportError(ctx, "TransferRoomOwnership", roomID, err)
		return &userpb.TransferRoomOwnershipResponse{Error: errDetail(errcode.InternalError, "failed to reach room")}, nil
	}
	if roomErr := roomResp.GetError(); roomErr != nil {
		_, detail := parseRoomError(ctx, "TransferRoomOwnership", roomID, roomErr)
		return &userpb.TransferRoomOwnershipResponse{Error: detail}, nil
	}
	return &userpb.TransferRoomOwnershipResponse{}, nil
}

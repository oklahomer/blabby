package room

import (
	"context"
	"errors"
	"log/slog"

	"github.com/asynkron/protoactor-go/cluster"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/id"
)

// SetMemberRole changes another member's role after the store confirms, in the
// same transaction as the write, that the actor's role permits it. Roles are not
// part of the grain's member cache and no event is journaled or fanned out — a
// role only matters the next time the database is asked about it.
func (g *Grain) SetMemberRole(req *roompb.SetMemberRoleRequest, ctx cluster.GrainContext) (*roompb.SetMemberRoleResponse, error) {
	if !g.state.isLoaded() {
		g.logRoleRejected(ctx, eventRoomRoleChangeRejected, "", errcode.RoomNotFound, nil)
		return setRoleErr(errcode.RoomNotFound, "room not found"), nil
	}
	actor, err := parseUserRef(req.GetActor())
	if err != nil {
		g.logRoleRejected(ctx, eventRoomRoleChangeRejected, req.GetActor().GetId(), errcode.InvalidRequest, err)
		return setRoleErr(errcode.InvalidRequest, "actor id and display name are required"), nil
	}
	targetID, err := id.ParseUserID(req.GetTargetUserId())
	if err != nil {
		g.logRoleRejected(ctx, eventRoomRoleChangeRejected, actor.ID().String(), errcode.InvalidRequest, err)
		return setRoleErr(errcode.InvalidRequest, "target_user_id is required"), nil
	}
	role, err := domain.ParseMembershipRole(req.GetRole())
	if err != nil || role == domain.MembershipRoleOwner {
		// The owner role never moves through a role change; requesting it is a
		// malformed request, not a permission question.
		g.logRoleRejected(ctx, eventRoomRoleChangeRejected, actor.ID().String(), errcode.InvalidRequest, err)
		return setRoleErr(errcode.InvalidRequest, "role must be admin or member"), nil
	}
	if !g.state.isMember(actor.ID()) {
		g.logRoleRejected(ctx, eventRoomRoleChangeRejected, actor.ID().String(), errcode.RoomNotMember, nil)
		return setRoleErr(errcode.RoomNotMember, "not a member of this room"), nil
	}
	if !g.state.isMember(targetID) {
		g.logRoleRejected(ctx, eventRoomRoleChangeRejected, actor.ID().String(), errcode.RoomNotMember, nil)
		return setRoleErr(errcode.RoomNotMember, "target is not a member of this room"), nil
	}
	if g.membership == nil {
		g.logRoleRejected(ctx, eventRoomRoleChangeRejected, actor.ID().String(), errcode.InternalError, nil)
		return setRoleErr(errcode.InternalError, "role management unavailable"), nil
	}

	err = g.membership.RecordRoleChange(context.Background(), g.state.roomRef().ID, actor.ID(), targetID, role)
	switch {
	case errors.Is(err, ErrRolePermissionDenied):
		g.logRoleRejected(ctx, eventRoomRoleChangeRejected, actor.ID().String(), errcode.RoomPermissionDenied, nil)
		return setRoleErr(errcode.RoomPermissionDenied, "your role does not permit changing member roles"), nil
	case err != nil:
		slog.Error(eventRoomMembershipWriteFailed,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", actor.ID(),
			"transition", "set_role",
			"error", err,
		)
		return setRoleErr(errcode.InternalError, "failed to record role change"), nil
	}

	slog.Info(eventRoomRoleChanged,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"user_id", actor.ID(),
		"target_id", targetID,
		"role", string(role),
	)
	return &roompb.SetMemberRoleResponse{}, nil
}

// TransferOwnership hands the room to another member: the store demotes the
// current owner to admin and promotes the target in one transaction, refusing
// unless the actor owns the room. Handing the room to the current owner is a
// successful no-op, so the operation is idempotent.
func (g *Grain) TransferOwnership(req *roompb.TransferOwnershipRequest, ctx cluster.GrainContext) (*roompb.TransferOwnershipResponse, error) {
	if !g.state.isLoaded() {
		g.logRoleRejected(ctx, eventRoomOwnershipTransferRejected, "", errcode.RoomNotFound, nil)
		return transferErr(errcode.RoomNotFound, "room not found"), nil
	}
	actor, err := parseUserRef(req.GetActor())
	if err != nil {
		g.logRoleRejected(ctx, eventRoomOwnershipTransferRejected, req.GetActor().GetId(), errcode.InvalidRequest, err)
		return transferErr(errcode.InvalidRequest, "actor id and display name are required"), nil
	}
	newOwnerID, err := id.ParseUserID(req.GetNewOwnerUserId())
	if err != nil {
		g.logRoleRejected(ctx, eventRoomOwnershipTransferRejected, actor.ID().String(), errcode.InvalidRequest, err)
		return transferErr(errcode.InvalidRequest, "new_owner_user_id is required"), nil
	}
	if !g.state.isMember(actor.ID()) {
		g.logRoleRejected(ctx, eventRoomOwnershipTransferRejected, actor.ID().String(), errcode.RoomNotMember, nil)
		return transferErr(errcode.RoomNotMember, "not a member of this room"), nil
	}
	if !g.state.isMember(newOwnerID) {
		g.logRoleRejected(ctx, eventRoomOwnershipTransferRejected, actor.ID().String(), errcode.RoomNotMember, nil)
		return transferErr(errcode.RoomNotMember, "new owner is not a member of this room"), nil
	}
	if g.membership == nil {
		g.logRoleRejected(ctx, eventRoomOwnershipTransferRejected, actor.ID().String(), errcode.InternalError, nil)
		return transferErr(errcode.InternalError, "role management unavailable"), nil
	}

	err = g.membership.RecordOwnershipTransfer(context.Background(), g.state.roomRef().ID, actor.ID(), newOwnerID)
	switch {
	case errors.Is(err, ErrRolePermissionDenied):
		g.logRoleRejected(ctx, eventRoomOwnershipTransferRejected, actor.ID().String(), errcode.RoomPermissionDenied, nil)
		return transferErr(errcode.RoomPermissionDenied, "only the owner can transfer ownership"), nil
	case err != nil:
		slog.Error(eventRoomMembershipWriteFailed,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", actor.ID(),
			"transition", "transfer_ownership",
			"error", err,
		)
		return transferErr(errcode.InternalError, "failed to record ownership transfer"), nil
	}

	slog.Info(eventRoomOwnershipTransferred,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"user_id", actor.ID(),
		"new_owner_id", newOwnerID,
	)
	return &roompb.TransferOwnershipResponse{}, nil
}

// logRoleRejected records one role-management refusal in the package's
// rejected-line shape. err is optional detail for parse failures.
func (g *Grain) logRoleRejected(ctx cluster.GrainContext, event, actorID string, code errcode.Code, err error) {
	attrs := []any{
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"user_id", actorID,
		"reason", code.Status(),
	}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	slog.Warn(event, attrs...)
}

func setRoleErr(code errcode.Code, msg string) *roompb.SetMemberRoleResponse {
	return &roompb.SetMemberRoleResponse{
		Error: &commonpb.ErrorDetail{Code: code.Int32(), Status: code.Status(), Message: msg},
	}
}

func transferErr(code errcode.Code, msg string) *roompb.TransferOwnershipResponse {
	return &roompb.TransferOwnershipResponse{
		Error: &commonpb.ErrorDetail{Code: code.Int32(), Status: code.Status(), Message: msg},
	}
}

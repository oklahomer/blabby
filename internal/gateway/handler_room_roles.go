package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/id"
)

const (
	// endpointRoomMemberRole sets a member's role within a room. {user} is the
	// target member's opaque U… code; the acting member is the authenticated
	// caller.
	endpointRoomMemberRole = "PUT /rooms/{id}/members/{user}/role"
	// endpointRoomOwner replaces the room's owner — modelled as a REST resource,
	// so handing the room to the current owner is an idempotent 200.
	endpointRoomOwner = "PUT /rooms/{id}/owner"

	// maxRoomCommandBodyBytes caps the tiny JSON bodies of the room command endpoints.
	maxRoomCommandBodyBytes = 1024
)

// UserResolver maps a client-facing U… public code to the internal UserID. The
// role endpoints use it to resolve the target member named in the request;
// *UserRepoDirectory is the production implementation.
type UserResolver interface {
	ResolveUserID(ctx context.Context, code id.PublicCode) (id.UserID, error)
}

// setRoleRequest is the JSON payload accepted by PUT /rooms/{id}/members/{user}/role.
type setRoleRequest struct {
	Role string `json:"role"`
}

// setOwnerRequest is the JSON payload accepted by PUT /rooms/{id}/owner.
type setOwnerRequest struct {
	User string `json:"user"`
}

// handleRoomMemberRolePut changes another member's role. The gateway resolves
// the R…/U… codes and forwards through the caller's User grain, which attaches
// the server-resolved actor identity; every role rule (who may set what) is
// enforced by the Room grain against the database.
func (g *Gateway) handleRoomMemberRolePut(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomMemberRole)
	if !ok {
		return
	}
	roomID, ok := g.requireRoomID(w, r, endpointRoomMemberRole, userID)
	if !ok {
		return
	}
	op := roomOp{endpoint: endpointRoomMemberRole, method: r.Method, userID: userID, roomID: roomID}
	targetID, ok := g.requireTargetUserID(w, r, op, r.PathValue("user"))
	if !ok {
		return
	}
	var req setRoleRequest
	if !decodeJSONBody(w, r, maxRoomCommandBodyBytes, &req) {
		return
	}
	if strings.TrimSpace(req.Role) == "" {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("role is required"))
		return
	}

	logRoomEntry(op)
	resp, err := g.userGrainFor(userID).SetRoomMemberRole(&userpb.SetRoomMemberRoleRequest{
		RoomId:       roomID.String(),
		TargetUserId: targetID.String(),
		Role:         req.Role,
	})
	if err != nil {
		logRoomTransportError(op)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to reach user grain"))
		return
	}
	if pe := resp.GetError(); pe != nil {
		writeBusinessErrorResponse(w, op, pe)
		return
	}
	logRoomExit(op, outcomeOK, 0)
	writeJSON(w, http.StatusOK, successResponse{Success: true})
}

// handleRoomOwnerPut hands the room to another member. Same shape as the role
// endpoint; the Room grain enforces that only the current owner may transfer.
func (g *Gateway) handleRoomOwnerPut(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomOwner)
	if !ok {
		return
	}
	roomID, ok := g.requireRoomID(w, r, endpointRoomOwner, userID)
	if !ok {
		return
	}
	op := roomOp{endpoint: endpointRoomOwner, method: r.Method, userID: userID, roomID: roomID}
	var req setOwnerRequest
	if !decodeJSONBody(w, r, maxRoomCommandBodyBytes, &req) {
		return
	}
	if strings.TrimSpace(req.User) == "" {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("user is required"))
		return
	}
	newOwnerID, ok := g.requireTargetUserID(w, r, op, req.User)
	if !ok {
		return
	}

	logRoomEntry(op)
	resp, err := g.userGrainFor(userID).TransferRoomOwnership(&userpb.TransferRoomOwnershipRequest{
		RoomId:         roomID.String(),
		NewOwnerUserId: newOwnerID.String(),
	})
	if err != nil {
		logRoomTransportError(op)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to reach user grain"))
		return
	}
	if pe := resp.GetError(); pe != nil {
		writeBusinessErrorResponse(w, op, pe)
		return
	}
	logRoomExit(op, outcomeOK, 0)
	writeJSON(w, http.StatusOK, successResponse{Success: true})
}

// requireTargetUserID parses a U… code and resolves it to the internal UserID.
// A malformed or unknown code is a 400: the opaque codes are unguessable, and a
// nonexistent user is by definition not a member of the room, so nothing is
// revealed that membership wouldn't.
func (g *Gateway) requireTargetUserID(w http.ResponseWriter, r *http.Request, op roomOp, raw string) (id.UserID, bool) {
	code, err := id.ParseUserCode(raw)
	if err != nil {
		slog.Warn("gateway.room.rejected",
			"endpoint", op.endpoint, "method", op.method,
			"user_id", op.userID, "reason", "invalid_user_code")
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("user id is invalid"))
		return id.UserID{}, false
	}
	targetID, err := g.users.ResolveUserID(r.Context(), code)
	switch {
	case errors.Is(err, auth.ErrPublicCodeUnknown):
		slog.Warn("gateway.room.rejected",
			"endpoint", op.endpoint, "method", op.method,
			"user_id", op.userID, "reason", "unknown_user_code")
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("unknown user"))
		return id.UserID{}, false
	case err != nil:
		logRoomTransportError(op)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to resolve user"))
		return id.UserID{}, false
	}
	return targetID, true
}

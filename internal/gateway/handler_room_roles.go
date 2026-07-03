package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

	// maxRoleBodyBytes caps the tiny JSON bodies of the role endpoints.
	maxRoleBodyBytes = 1024
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
	targetID, ok := g.requireTargetUserID(w, r, endpointRoomMemberRole, userID, r.PathValue("user"))
	if !ok {
		return
	}
	var req setRoleRequest
	if !decodeRoleBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Role) == "" {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("role is required"))
		return
	}

	logRoomEntry(endpointRoomMemberRole, r.Method, userID, roomID)
	resp, err := g.userGrainFor(userID).SetRoomMemberRole(&userpb.SetRoomMemberRoleRequest{
		RoomId:       roomID.String(),
		TargetUserId: targetID.String(),
		Role:         req.Role,
	})
	if err != nil {
		logRoomTransportError(endpointRoomMemberRole, r.Method, userID, roomID)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to reach user grain"))
		return
	}
	if pe := resp.GetError(); pe != nil {
		writeBusinessErrorResponse(w, endpointRoomMemberRole, r.Method, userID, roomID, pe)
		return
	}
	logRoomExit(endpointRoomMemberRole, r.Method, userID, roomID, outcomeOK, 0)
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
	var req setOwnerRequest
	if !decodeRoleBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.User) == "" {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("user is required"))
		return
	}
	newOwnerID, ok := g.requireTargetUserID(w, r, endpointRoomOwner, userID, req.User)
	if !ok {
		return
	}

	logRoomEntry(endpointRoomOwner, r.Method, userID, roomID)
	resp, err := g.userGrainFor(userID).TransferRoomOwnership(&userpb.TransferRoomOwnershipRequest{
		RoomId:         roomID.String(),
		NewOwnerUserId: newOwnerID.String(),
	})
	if err != nil {
		logRoomTransportError(endpointRoomOwner, r.Method, userID, roomID)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to reach user grain"))
		return
	}
	if pe := resp.GetError(); pe != nil {
		writeBusinessErrorResponse(w, endpointRoomOwner, r.Method, userID, roomID, pe)
		return
	}
	logRoomExit(endpointRoomOwner, r.Method, userID, roomID, outcomeOK, 0)
	writeJSON(w, http.StatusOK, successResponse{Success: true})
}

// requireTargetUserID parses a U… code and resolves it to the internal UserID.
// A malformed or unknown code is a 400: the opaque codes are unguessable, and a
// nonexistent user is by definition not a member of the room, so nothing is
// revealed that membership wouldn't.
func (g *Gateway) requireTargetUserID(w http.ResponseWriter, r *http.Request, endpoint string, actorID id.UserID, raw string) (id.UserID, bool) {
	code, err := id.ParseUserCode(raw)
	if err != nil {
		slog.Warn("room handler rejected request",
			"endpoint", endpoint, "method", r.Method,
			"user_id", actorID, "reason", "invalid_user_code")
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("user id is invalid"))
		return id.UserID{}, false
	}
	targetID, err := g.users.ResolveUserID(r.Context(), code)
	switch {
	case errors.Is(err, auth.ErrPublicCodeUnknown):
		slog.Warn("room handler rejected request",
			"endpoint", endpoint, "method", r.Method,
			"user_id", actorID, "reason", "unknown_user_code")
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("unknown user"))
		return id.UserID{}, false
	case err != nil:
		logRoomTransportError(endpoint, r.Method, actorID, id.RoomID{})
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to resolve user"))
		return id.UserID{}, false
	}
	return targetID, true
}

// decodeRoleBody decodes one of the role endpoints' tiny JSON bodies with the
// package's strict rules (JSON content type, size cap, no trailing data),
// writing the rejection itself and returning false on failure.
func decodeRoleBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !contentTypeIsJSON(r.Header.Get("Content-Type")) {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("content-type must be application/json"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRoleBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			WriteErrorResponse(w, http.StatusRequestEntityTooLarge, ErrPayloadTooLarge("request body exceeds maximum size"))
			return false
		}
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("malformed request body"))
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("malformed request body"))
		return false
	}
	return true
}

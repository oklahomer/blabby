package gateway

import (
	"log/slog"
	"net/http"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

// endpointRoomCreate creates a room owned by the authenticated caller.
const endpointRoomCreate = "POST /rooms"

// createRoomRequest is the JSON payload accepted by POST /rooms.
type createRoomRequest struct {
	Name string `json:"name"`
}

// handleRoomCreate creates a room with the caller as its owner and returns the
// new room's descriptor. After the create commits, it makes a best-effort
// JoinRoom round-trip through the caller's User grain: the Room grain hydrates
// the owner membership from the database and answers ROOM_ALREADY_MEMBER, which
// the User grain's repair path turns into a cached joined-room entry — so the
// new room shows up in GET /rooms/joined immediately instead of after the
// grain's next activation. A failed warm-up is logged and the create still
// succeeds; the cache self-heals on reactivation.
func (g *Gateway) handleRoomCreate(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomCreate)
	if !ok {
		return
	}
	var req createRoomRequest
	if !decodeRoomCommandBody(w, r, &req) {
		return
	}
	name, err := domain.NewRoomName(req.Name)
	if err != nil {
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("name must be 1-64 bytes of printable characters"))
		return
	}

	logRoomEntry(endpointRoomCreate, r.Method, userID, id.RoomID{})
	info, err := g.roomCreator.CreateRoom(r.Context(), userID, name)
	if err != nil {
		slog.Error("room creation failed", "user_id", userID, "error", err.Error())
		WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("room creation unavailable"))
		return
	}

	if _, err := g.userGrainFor(userID).JoinRoom(&userpb.JoinRoomRequest{RoomId: info.ID.String()}); err != nil {
		slog.Warn("room creation: joined-rooms cache warm-up failed",
			"user_id", userID, "room_id", info.ID, "error", err)
	}

	logRoomExit(endpointRoomCreate, r.Method, userID, info.ID, outcomeOK, 0)
	writeJSON(w, http.StatusCreated, roomDescriptor{ID: info.PublicID(), Name: info.Name})
}

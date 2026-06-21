package gateway

import (
	"fmt"
	"log/slog"
	"net/http"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/id"
)

// roomDescriptor is the on-the-wire shape of one room: the opaque public code the
// client addresses the room by (R…) and its display name. No internal numeric id
// is exposed.
type roomDescriptor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// roomListResponse is the body of both GET /rooms (the catalogue) and
// GET /rooms/joined (the user's rooms): a list of room descriptors.
type roomListResponse struct {
	Rooms []roomDescriptor `json:"rooms"`
}

// handleRoomList returns the active-room catalogue as R… descriptors, resolved
// through the room directory (the database). The slice is explicitly initialised
// so an empty result marshals as `[]`, never `null`.
func (g *Gateway) handleRoomList(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomList)
	if !ok {
		return
	}
	logRoomEntry(endpointRoomList, r.Method, userID, id.RoomID{})

	rooms, err := g.rooms.ListActive(r.Context())
	if err != nil {
		slog.Warn("room handler transport error",
			"endpoint", endpointRoomList, "method", r.Method,
			"user_id", userID, "outcome", outcomeTransportError)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to list rooms"))
		return
	}

	logRoomExit(endpointRoomList, r.Method, userID, id.RoomID{}, outcomeOK, 0)
	writeJSON(w, http.StatusOK, roomListResponse{Rooms: toDescriptors(rooms)})
}

// handleRoomJoined returns the rooms the authenticated user has joined, as R…
// descriptors. The User grain reports internal ids; the gateway resolves them to
// public codes + names via the directory, preserving the grain's order and
// dropping any room that is no longer active. The slice is explicitly initialised
// so an empty result marshals as `[]`, never `null`.
func (g *Gateway) handleRoomJoined(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomJoined)
	if !ok {
		return
	}
	logRoomEntry(endpointRoomJoined, r.Method, userID, id.RoomID{})

	resp, err := g.userGrainFor(userID).GetJoinedRooms(&userpb.GetJoinedRoomsRequest{})
	if err != nil {
		slog.Warn("room handler transport error",
			"endpoint", endpointRoomJoined, "method", r.Method,
			"user_id", userID, "outcome", outcomeTransportError)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to reach user grain"))
		return
	}

	joinedIDs, err := parseRoomIDs(resp.GetRoomIds())
	if err != nil {
		// The User grain is contracted to return internal decimal ids; an
		// unparseable one is a server-side bug, so fail closed rather than let a
		// room silently vanish from the user's list.
		slog.Error("room handler internal error",
			"endpoint", endpointRoomJoined, "method", r.Method,
			"user_id", userID, "error", err)
		WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("failed to read joined rooms"))
		return
	}
	infos, err := g.rooms.Describe(r.Context(), joinedIDs)
	if err != nil {
		slog.Warn("room handler transport error",
			"endpoint", endpointRoomJoined, "method", r.Method,
			"user_id", userID, "outcome", outcomeTransportError)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to resolve joined rooms"))
		return
	}

	logRoomExit(endpointRoomJoined, r.Method, userID, id.RoomID{}, outcomeOK, 0)
	writeJSON(w, http.StatusOK, roomListResponse{Rooms: toDescriptors(infos)})
}

// parseRoomIDs converts the grain's internal decimal room ids into typed RoomIDs.
// An unparseable value is a grain contract violation; it returns an error so the
// caller can fail closed rather than silently drop the room.
func parseRoomIDs(raw []string) ([]id.RoomID, error) {
	out := make([]id.RoomID, len(raw))
	for i, s := range raw {
		roomID, err := id.ParseRoomID(s)
		if err != nil {
			return nil, fmt.Errorf("user grain returned an unparseable room id %q: %w", s, err)
		}
		out[i] = roomID
	}
	return out, nil
}

// toDescriptors renders rooms as their client-facing R… descriptors.
func toDescriptors(rooms []RoomInfo) []roomDescriptor {
	out := make([]roomDescriptor, 0, len(rooms))
	for _, info := range rooms {
		out = append(out, roomDescriptor{ID: info.PublicID(), Name: info.Name})
	}
	return out
}

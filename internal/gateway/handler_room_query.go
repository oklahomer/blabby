package gateway

import (
	"log/slog"
	"net/http"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/id"
)

// roomDescriptor is the on-the-wire shape of one entry in the room list.
type roomDescriptor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// defaultRooms is the interim hardcoded room catalogue. The ids are the decimal
// Snowflakes the persistence seed assigns the dev rooms (room 4/5 in schema.sql),
// so joining a catalogue entry addresses the same room the database knows; a
// DB-backed catalogue replaces this slice in a later phase. The slice is treated
// as effectively immutable — never sort or mutate in place because handlers may
// run concurrently.
var defaultRooms = []roomDescriptor{
	{ID: "4", Name: "General"},
	{ID: "5", Name: "Random"},
}

type roomListResponse struct {
	Rooms []roomDescriptor `json:"rooms"`
}

type joinedRoomsResponse struct {
	RoomIDs []string `json:"room_ids"`
}

// handleRoomList returns the static room catalogue. It does not
// dispatch any cluster RPC — the list is served entirely from the
// in-process defaultRooms slice.
func (g *Gateway) handleRoomList(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomList)
	if !ok {
		return
	}
	logRoomEntry(endpointRoomList, r.Method, userID, id.RoomID{})
	logRoomExit(endpointRoomList, r.Method, userID, id.RoomID{}, outcomeOK, 0)
	writeJSON(w, http.StatusOK, roomListResponse{Rooms: defaultRooms})
}

// handleRoomJoined returns the rooms the authenticated user has
// joined, as observed by the User grain. The returned slice is
// explicitly initialised so an empty result marshals as `[]`, never
// `null`.
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

	// Copy the slice so the JSON envelope never aliases the proto
	// message's internal storage. Also normalises a nil proto slice to
	// an empty JSON array rather than `null`.
	roomIDs := append([]string{}, resp.GetRoomIds()...)
	logRoomExit(endpointRoomJoined, r.Method, userID, id.RoomID{}, outcomeOK, 0)
	writeJSON(w, http.StatusOK, joinedRoomsResponse{RoomIDs: roomIDs})
}

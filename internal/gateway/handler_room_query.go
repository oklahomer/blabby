package gateway

import (
	"log/slog"
	"net/http"

	userpb "github.com/oklahomer/blabby/gen/user"
)

// roomDescriptor is the on-the-wire shape of one entry in the room list.
type roomDescriptor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// defaultRooms is the Phase 1 hardcoded room list. The list is short
// and intentionally so; production room provisioning is deferred to a
// future epic. The slice is treated as effectively immutable — never
// sort or mutate in place because handlers may run concurrently.
var defaultRooms = []roomDescriptor{
	{ID: "general", Name: "General"},
	{ID: "random", Name: "Random"},
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
	logRoomEntry(endpointRoomList, r.Method, userID, "")
	logRoomExit(endpointRoomList, r.Method, userID, "", outcomeOK, 0)
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

	logRoomEntry(endpointRoomJoined, r.Method, userID, "")
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
	ids := append([]string{}, resp.GetRoomIds()...)
	logRoomExit(endpointRoomJoined, r.Method, userID, "", outcomeOK, 0)
	writeJSON(w, http.StatusOK, joinedRoomsResponse{RoomIDs: ids})
}

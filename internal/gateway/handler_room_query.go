package gateway

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
)

// roomDescriptor is the on-the-wire shape of one room: the opaque public code the
// client addresses the room by (R…) and its display name. No internal numeric id
// is exposed.
type roomDescriptor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// roomListResponse is the body of GET /rooms/joined: the user's rooms as a
// list of room descriptors.
type roomListResponse struct {
	Rooms []roomDescriptor `json:"rooms"`
}

// roomCataloguePage is the body of GET /rooms: one page of the catalogue plus
// the continuation cursor. Next is the `after` value for the following page —
// the R… id of the page's last descriptor — or null when the listing is
// exhausted.
type roomCataloguePage struct {
	Rooms []roomDescriptor `json:"rooms"`
	Next  *string          `json:"next"`
}

// Bounds for the GET /rooms `limit` query parameter.
const (
	roomListDefaultLimit = 50
	roomListMaxLimit     = 200
)

// roomListParams is the parsed form of GET /rooms' query parameters. The zero
// values of query and after mean "no name filter" and "first page"; after is
// the still-unresolved R… cursor because resolving it to an internal id takes
// a directory lookup, which stays with the handler.
type roomListParams struct {
	query domain.RoomNameQuery
	after id.PublicCode
	limit int
}

// parseRoomListQuery parses and validates the q / after / limit query
// parameters. A blank q or after is treated as absent; limit defaults to
// roomListDefaultLimit and is rejected outside [1, roomListMaxLimit].
func parseRoomListQuery(r *http.Request) (roomListParams, *roomRequestError) {
	params := roomListParams{limit: roomListDefaultLimit}
	values := r.URL.Query()

	if raw := strings.TrimSpace(values.Get("q")); raw != "" {
		query, err := domain.NewRoomNameQuery(raw)
		if err != nil {
			return roomListParams{}, &roomRequestError{
				reason: "invalid_query",
				detail: ErrInvalidRequest("q must be 1-64 bytes of printable characters"),
			}
		}
		params.query = query
	}
	if raw := strings.TrimSpace(values.Get("after")); raw != "" {
		code, err := id.ParseRoomCode(raw)
		if err != nil {
			return roomListParams{}, &roomRequestError{
				reason: "invalid_after",
				detail: ErrInvalidRequest("after is not a valid room code"),
			}
		}
		params.after = code
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > roomListMaxLimit {
			return roomListParams{}, &roomRequestError{
				reason: "invalid_limit",
				detail: ErrInvalidRequest(fmt.Sprintf("limit must be an integer between 1 and %d", roomListMaxLimit)),
			}
		}
		params.limit = limit
	}
	return params, nil
}

// handleRoomList returns one page of the active-room catalogue as R…
// descriptors, resolved through the room directory (the database). The page is
// narrowed by `q` (case-insensitive literal substring on the display name) and
// continued with `after`/`limit` keyset pagination; the response's `next`
// cursor is null once the listing is exhausted. The slice is explicitly
// initialised so an empty result marshals as `[]`, never `null`.
func (g *Gateway) handleRoomList(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomList)
	if !ok {
		return
	}
	params, perr := parseRoomListQuery(r)
	if perr != nil {
		slog.Warn("room handler rejected request",
			"endpoint", endpointRoomList, "method", r.Method,
			"user_id", userID, "reason", perr.reason)
		WriteErrorResponse(w, httpStatus(perr.detail.Code), perr.detail)
		return
	}
	logRoomEntry(endpointRoomList, r.Method, userID, id.RoomID{})

	query := ListActiveQuery{Query: params.query, Limit: params.limit}
	if !params.after.IsZero() {
		after, err := g.rooms.Resolve(r.Context(), params.after)
		switch {
		case errors.Is(err, persistence.ErrRoomNotFound):
			// A stale or bogus continuation, not a missing resource: the client
			// restarts from the first page.
			slog.Warn("room handler rejected request",
				"endpoint", endpointRoomList, "method", r.Method,
				"user_id", userID, "reason", "unknown_after")
			WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("after references an unknown room"))
			return
		case err != nil:
			logRoomTransportError(endpointRoomList, r.Method, userID, id.RoomID{})
			WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to resolve after cursor"))
			return
		}
		query.After = after
	}

	page, err := g.rooms.ListActive(r.Context(), query)
	if err != nil {
		slog.Warn("room handler transport error",
			"endpoint", endpointRoomList, "method", r.Method,
			"user_id", userID, "outcome", outcomeTransportError)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to list rooms"))
		return
	}

	resp := roomCataloguePage{Rooms: toDescriptors(page.Rooms)}
	if page.HasMore {
		next := page.Rooms[len(page.Rooms)-1].PublicID()
		resp.Next = &next
	}
	logRoomExit(endpointRoomList, r.Method, userID, id.RoomID{}, outcomeOK, 0)
	writeJSON(w, http.StatusOK, resp)
}

// handleRoomJoined returns the rooms the authenticated user has joined, as R…
// descriptors rendered directly from the reference metadata the User grain
// caches — no per-request room-repository lookup. The grain's order is preserved.
// The slice is explicitly initialised so an empty result marshals as `[]`, never
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

	descriptors, err := descriptorsFromRefs(resp.GetRooms())
	if err != nil {
		// The User grain is contracted to return well-formed room refs; an
		// unparseable public code is a server-side bug, so fail closed rather than
		// let a room silently vanish from the user's list.
		slog.Error("room handler internal error",
			"endpoint", endpointRoomJoined, "method", r.Method,
			"user_id", userID, "error", err)
		WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("failed to read joined rooms"))
		return
	}

	logRoomExit(endpointRoomJoined, r.Method, userID, id.RoomID{}, outcomeOK, 0)
	writeJSON(w, http.StatusOK, roomListResponse{Rooms: descriptors})
}

// descriptorsFromRefs renders cached room refs as their client-facing R…
// descriptors. An unparseable public code is a grain contract violation; it
// returns an error so the caller can fail closed rather than silently drop the
// room. The slice is explicitly initialised so an empty result marshals as `[]`.
func descriptorsFromRefs(refs []*commonpb.RoomRef) ([]roomDescriptor, error) {
	out := make([]roomDescriptor, len(refs))
	for i, ref := range refs {
		code, err := id.ParsePublicCode(ref.GetPublicCode())
		if err != nil {
			return nil, fmt.Errorf("user grain returned an unparseable room public_code %q: %w", ref.GetPublicCode(), err)
		}
		out[i] = roomDescriptor{ID: code.FormatRoom(), Name: ref.GetName()}
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

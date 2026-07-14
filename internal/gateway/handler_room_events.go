package gateway

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
)

// endpointRoomEvents doubles as the mux pattern and the structured-log
// endpoint field, like every room endpoint label.
const endpointRoomEvents = "GET /rooms/{id}/events"

// eventPerson is the on-the-wire author reference on a timeline event: the
// opaque public user code (U…) and the current display name. No internal
// numeric id is exposed.
type eventPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// roomEvent is one timeline entry on the wire. Type discriminates the shape,
// mirroring the WS frames' vocabulary for people: a "message" carries
// sender+text, while "member_joined"/"member_left" carry user. Timestamp is
// unix milliseconds, matching the message frame.
type roomEvent struct {
	ID        string       `json:"id"`
	Type      string       `json:"type"`
	Sender    *eventPerson `json:"sender,omitempty"`
	User      *eventPerson `json:"user,omitempty"`
	Text      string       `json:"text,omitempty"`
	Timestamp int64        `json:"timestamp"`
}

// roomEventsPage is the body of GET /rooms/{id}/events: one newest-first page
// of the room's timeline plus the continuation cursor. Next is the `before`
// value for the following (older) page — the id of the page's last event — or
// null when the history is exhausted.
type roomEventsPage struct {
	Events []roomEvent `json:"events"`
	Next   *string     `json:"next"`
}

// Bounds for the GET /rooms/{id}/events `limit` query parameter.
const (
	roomEventsDefaultLimit = 50
	roomEventsMaxLimit     = 200
)

// roomEventsParams is the parsed form of GET /rooms/{id}/events' query
// parameters. The zero values of query and before mean "no message-text
// filter" and "newest page".
type roomEventsParams struct {
	query  domain.MessageQuery
	before id.EventID
	limit  int
}

// parseRoomEventsQuery parses and validates the q / before / limit query
// parameters. A blank q or before is treated as absent; limit defaults to
// roomEventsDefaultLimit and is rejected outside [1, roomEventsMaxLimit].
func parseRoomEventsQuery(r *http.Request) (roomEventsParams, *requestError) {
	params := roomEventsParams{limit: roomEventsDefaultLimit}
	values := r.URL.Query()

	if raw := strings.TrimSpace(values.Get("q")); raw != "" {
		query, err := domain.NewMessageQuery(raw)
		if err != nil {
			return roomEventsParams{}, &requestError{
				reason: "invalid_query",
				detail: ErrInvalidRequest("q must be 1-256 bytes of valid UTF-8"),
			}
		}
		params.query = query
	}
	if raw := strings.TrimSpace(values.Get("before")); raw != "" {
		before, err := id.ParseEventID(raw)
		if err != nil {
			return roomEventsParams{}, &requestError{
				reason: "invalid_before",
				detail: ErrInvalidRequest("before is not a valid event id"),
			}
		}
		params.before = before
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > roomEventsMaxLimit {
			return roomEventsParams{}, &requestError{
				reason: "invalid_limit",
				detail: ErrInvalidRequest(fmt.Sprintf("limit must be an integer between 1 and %d", roomEventsMaxLimit)),
			}
		}
		params.limit = limit
	}
	return params, nil
}

// handleRoomEvents returns one newest-first page of the room's timeline —
// messages and membership events interleaved — to a current member. The page
// is narrowed by `q` (full-text over message text; every whitespace-separated
// term must match, literally) and continued with `before`/`limit` keyset
// pagination; the response's `next` cursor is null once the history is
// exhausted. The slice is explicitly initialised so an empty result marshals
// as `[]`, never `null`.
func (g *Gateway) handleRoomEvents(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomEvents)
	if !ok {
		return
	}
	roomID, ok := g.requireRoomID(w, r, endpointRoomEvents, userID)
	if !ok {
		return
	}
	params, perr := parseRoomEventsQuery(r)
	if perr != nil {
		slog.Warn("gateway.room.rejected",
			"endpoint", endpointRoomEvents, "method", r.Method,
			"user_id", userID, "room_id", roomID, "reason", perr.reason)
		WriteErrorResponse(w, httpStatus(perr.detail.Code), perr.detail)
		return
	}
	logRoomEntry(endpointRoomEvents, r.Method, userID, roomID)

	member, err := g.timeline.IsMember(r.Context(), roomID, userID)
	if err != nil {
		logRoomTransportError(endpointRoomEvents, r.Method, userID, roomID)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to check membership"))
		return
	}
	if !member {
		slog.Warn("gateway.room.rejected",
			"endpoint", endpointRoomEvents, "method", r.Method,
			"user_id", userID, "room_id", roomID, "reason", "not_member")
		WriteErrorResponse(w, http.StatusForbidden, ErrRoomNotMember("not a member of this room"))
		return
	}

	page, err := g.timeline.Events(r.Context(), TimelineQuery{
		RoomID: roomID,
		Query:  params.query,
		Before: params.before,
		Limit:  params.limit,
	})
	if err != nil {
		logRoomTransportError(endpointRoomEvents, r.Method, userID, roomID)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to load events"))
		return
	}

	events, err := toRoomEvents(page.Events)
	if err != nil {
		// An unmapped entry kind is a server-side bug (see toRoomEvents), so
		// fail closed rather than serve a page with an untyped event.
		slog.Error("gateway.room.internal_error",
			"endpoint", endpointRoomEvents, "method", r.Method,
			"user_id", userID, "room_id", roomID, "error", err)
		WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("failed to render events"))
		return
	}

	resp := roomEventsPage{Events: events}
	if page.HasMore {
		next := page.Events[len(page.Events)-1].ID.String()
		resp.Next = &next
	}
	logRoomExit(endpointRoomEvents, r.Method, userID, roomID, outcomeOK, 0)
	writeJSON(w, http.StatusOK, resp)
}

// toRoomEvents renders timeline entries in their wire shape: users as U…
// codes, message text on messages only, and the person under sender (message)
// or user (membership event), mirroring the WS frames. An entry kind without a
// wire mapping — a kind added to the journal but not here — is an error so the
// caller can fail closed rather than emit an untyped event; the compiler
// cannot flag the non-exhaustive switch when the enum grows.
func toRoomEvents(entries []persistence.TimelineEntry) ([]roomEvent, error) {
	out := make([]roomEvent, len(entries))
	for i, entry := range entries {
		person := &eventPerson{ID: entry.User.Code.FormatUser(), Name: entry.User.Name}
		event := roomEvent{
			ID:        entry.ID.String(),
			Timestamp: entry.OccurredAt.UnixMilli(),
		}
		switch entry.Kind {
		case persistence.EntryMessage:
			event.Type = "message"
			event.Sender = person
			event.Text = entry.Text
		case persistence.EntryMemberJoined:
			event.Type = "member_joined"
			event.User = person
		case persistence.EntryMemberLeft:
			event.Type = "member_left"
			event.User = person
		default:
			return nil, fmt.Errorf("journal entry %s has no wire mapping for kind %d", entry.ID, entry.Kind)
		}
		out[i] = event
	}
	return out, nil
}

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TimelineKind discriminates the entries GET /rooms/{id}/events returns.
// The HTTP timeline names them message / member_joined / member_left;
// LoadEventsCmd normalises those into these three values, matching the
// joined/left vocabulary the WebSocket frames use.
type TimelineKind int

const (
	// TimelineMessage is a posted chat message (carries Person + Text).
	TimelineMessage TimelineKind = iota
	// TimelineJoined is a member-joined system entry (carries Person).
	TimelineJoined
	// TimelineLeft is a member-left system entry (carries Person).
	TimelineLeft
)

// TimelineEvent is one parsed entry from a room's history page. Person is
// the message sender (TimelineMessage) or the member the event applies to
// (TimelineJoined / TimelineLeft). Text is set only for TimelineMessage.
// EventID is the entry's numeric ordering key, parsed from the wire's
// decimal Snowflake string.
type TimelineEvent struct {
	EventID int64
	Kind    TimelineKind
	Person  UserRef
	Text    string
	At      time.Time
}

// RoomEventsLoaded is emitted by LoadEventsCmd on HTTP 200 with one page
// of a room's timeline, parsed in the order the server served it
// (newest-first). Next is the `before` cursor for the following (older)
// page, "" when the history is exhausted. Before echoes the dispatched
// cursor so the root Model can tell a newest-page load ("") from an
// older-page load and apply the right pagination rule.
type RoomEventsLoaded struct {
	RoomID     string
	Events     []TimelineEvent
	Next       string
	Before     string
	Generation SessionGeneration
}

// RoomEventsLoadFailed is emitted by LoadEventsCmd for every non-success
// outcome — error envelopes, malformed or contract-violating bodies,
// network failures. Fields follow the same convention as
// RoomsLoadFailed; Before echoes the dispatched cursor.
type RoomEventsLoadFailed struct {
	RoomID     string
	Status     string
	Message    string
	HTTPStatus int
	Before     string
	Generation SessionGeneration
}

// LoadEventsCmd performs GET {server}/rooms/{roomID}/events with the
// bearer header and emits exactly one outbound tea.Msg describing the
// outcome. before, when non-empty, is the keyset cursor for the next
// older page; an empty before requests the newest page. The server's
// default limit (50) is used — no limit parameter is sent. The token
// never appears outside the Authorization header.
func LoadEventsCmd(client *http.Client, server, token, roomID, before string, generation SessionGeneration, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		path := "/rooms/" + url.PathEscape(roomID) + "/events"
		if before != "" {
			params := url.Values{}
			params.Set("before", before)
			path += "?" + params.Encode()
		}
		raw, httpStatus, err := doRoomRequest(client, http.MethodGet, server, path, token, nil, timeout)
		if err != nil {
			return RoomEventsLoadFailed{RoomID: roomID, Message: err.Error(), Before: before, Generation: generation}
		}
		if httpStatus == http.StatusOK {
			var resp RoomEventsResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return RoomEventsLoadFailed{
					RoomID:     roomID,
					Message:    fmt.Sprintf("decode room events: %s", err.Error()),
					HTTPStatus: httpStatus,
					Before:     before,
					Generation: generation,
				}
			}
			events, err := parseTimelineEvents(resp.Events)
			if err != nil {
				return RoomEventsLoadFailed{
					RoomID:     roomID,
					Message:    fmt.Sprintf("decode room events: %s", err.Error()),
					HTTPStatus: httpStatus,
					Before:     before,
					Generation: generation,
				}
			}
			next := ""
			if resp.Next != nil {
				next = *resp.Next
			}
			return RoomEventsLoaded{RoomID: roomID, Events: events, Next: next, Before: before, Generation: generation}
		}
		status, message := parseErrorEnvelope(raw, httpStatus)
		return RoomEventsLoadFailed{
			RoomID:     roomID,
			Status:     status,
			Message:    message,
			HTTPStatus: httpStatus,
			Before:     before,
			Generation: generation,
		}
	}
}

// parseTimelineEvents converts one history page's wire events into ordered
// TimelineEvents. A known type missing its person, or carrying an
// unparseable id, fails the whole page: a 200 body that violates the
// contract is a server fault the client surfaces rather than papering
// over with a partial page. An unknown type is skipped so a future event
// kind the client predates does not break history loading.
func parseTimelineEvents(raw []RoomEvent) ([]TimelineEvent, error) {
	out := make([]TimelineEvent, 0, len(raw))
	for _, e := range raw {
		var kind TimelineKind
		var person UserRef
		switch e.Type {
		case "message":
			if e.Sender == nil {
				return nil, fmt.Errorf("event %q: message without sender", e.ID)
			}
			kind, person = TimelineMessage, *e.Sender
		case "member_joined":
			if e.User == nil {
				return nil, fmt.Errorf("event %q: member_joined without user", e.ID)
			}
			kind, person = TimelineJoined, *e.User
		case "member_left":
			if e.User == nil {
				return nil, fmt.Errorf("event %q: member_left without user", e.ID)
			}
			kind, person = TimelineLeft, *e.User
		default:
			continue
		}
		eventID, err := parseWireEventID(e.ID)
		if err != nil {
			return nil, fmt.Errorf("event %q: %w", e.ID, err)
		}
		out = append(out, TimelineEvent{
			EventID: eventID,
			Kind:    kind,
			Person:  person,
			Text:    e.Text,
			At:      millisToTime(e.Timestamp),
		})
	}
	return out, nil
}

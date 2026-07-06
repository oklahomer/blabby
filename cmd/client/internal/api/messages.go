package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/timeline"
)

// SendMessageSucceeded is emitted by SendMessageCmd on HTTP 200. At is
// the server-assigned post time parsed from the response's Unix-ms
// timestamp; it is a zero time.Time when the server reported 0. The
// rendered message arrives separately as the echoed WebSocket frame —
// this message only acknowledges the post.
type SendMessageSucceeded struct {
	RoomID     string
	Generation SessionGeneration
	At         time.Time
}

// SendMessageFailed is emitted by SendMessageCmd for every non-success
// outcome — error envelopes, malformed bodies, network failures. Status
// is empty for transport errors and for bodies without a parseable
// envelope. HTTPStatus is 0 for transport failures and the response's
// status otherwise; the root Model treats 401 as session expiry. Text
// echoes the attempted message body so the root Model can restore it
// into the composer for a retry.
type SendMessageFailed struct {
	RoomID     string
	Generation SessionGeneration
	Status     string
	Message    string
	Text       string
	HTTPStatus int
}

// ChatMessageReceived is the decoded form of an inbound {"type":"message"}
// frame. EventID is the message's timeline id, parsed from the frame's
// decimal Snowflake string; it orders and dedups the message in the
// scrollback. At is the sender's post time parsed from the frame's
// Unix-ms timestamp (zero time.Time when the server emitted 0).
type ChatMessageReceived struct {
	RoomID  string
	Sender  UserRef
	Text    string
	EventID timeline.EventID
	At      time.Time
}

// MemberEventKind discriminates the two membership system events a room
// timeline carries.
type MemberEventKind int

const (
	// MemberJoined marks a member joining the room (WS "joined" frame,
	// HTTP "member_joined" timeline entry).
	MemberJoined MemberEventKind = iota
	// MemberLeft marks a member leaving the room (WS "left" frame,
	// HTTP "member_left" timeline entry).
	MemberLeft
)

// MemberEventReceived is the decoded form of an inbound {"type":"joined"}
// or {"type":"left"} frame. EventID is the membership event's timeline
// id, parsed from the frame's decimal Snowflake string; it orders and
// dedups the system line in the scrollback. At is the event's
// server-assigned time (zero time.Time when the server emitted 0).
type MemberEventReceived struct {
	RoomID  string
	User    UserRef
	Kind    MemberEventKind
	EventID timeline.EventID
	At      time.Time
}

// ErrorFrameReceived is the decoded form of an inbound {"type":"error"}
// frame — the generic async, non-auth error in the WebSocket contract
// (server type connection.ErrorResponse). The root Model humanises
// Status/Message into the inline Main-pane error.
type ErrorFrameReceived struct {
	Status  string
	Message string
}

// SendMessageCommandRequest groups the inputs for SendMessageCmd.
type SendMessageCommandRequest struct {
	Client     *http.Client
	Server     string
	Token      string
	Generation SessionGeneration
	RoomID     string
	Text       string
	Timeout    time.Duration
}

// SendMessageCmd performs POST {server}/rooms/{roomID}/messages with the
// bearer header and a {"text":...} body, emitting exactly one outbound
// tea.Msg describing the outcome. The token never appears outside the
// Authorization header — neither SendMessageSucceeded nor
// SendMessageFailed carries it.
//
// On HTTP 200 the Unix-ms timestamp is parsed into a time.Time here, at
// the boundary, so internal code only ever sees time.Time.
func SendMessageCmd(req SendMessageCommandRequest) tea.Cmd {
	return func() tea.Msg {
		body, err := json.Marshal(SendMessageRequestBody{Text: req.Text})
		if err != nil {
			return SendMessageFailed{RoomID: req.RoomID, Generation: req.Generation, Message: err.Error(), Text: req.Text}
		}
		path := "/rooms/" + url.PathEscape(req.RoomID) + "/messages"
		raw, httpStatus, err := doRoomRequest(req.Client, http.MethodPost, req.Server, path, req.Token, body, req.Timeout)
		if err != nil {
			return SendMessageFailed{RoomID: req.RoomID, Generation: req.Generation, Message: err.Error(), Text: req.Text}
		}
		if httpStatus == http.StatusOK {
			var resp SendMessageResponse
			if err := json.Unmarshal(raw, &resp); err != nil || !resp.Success {
				return SendMessageFailed{
					RoomID:     req.RoomID,
					Generation: req.Generation,
					Message:    sendResponseFailureMessage(err),
					Text:       req.Text,
					HTTPStatus: httpStatus,
				}
			}
			return SendMessageSucceeded{RoomID: req.RoomID, Generation: req.Generation, At: millisToTime(resp.Timestamp)}
		}
		status, message := parseErrorEnvelope(raw, httpStatus)
		return SendMessageFailed{
			RoomID:     req.RoomID,
			Generation: req.Generation,
			Status:     status,
			Message:    message,
			Text:       req.Text,
			HTTPStatus: httpStatus,
		}
	}
}

func sendResponseFailureMessage(err error) string {
	if err != nil {
		return "decode send response: " + err.Error()
	}
	return "server reported send with no success flag"
}

// DecodeChatMessage parses a raw inbound frame as a {"type":"message"}
// chat frame. It returns ok=false for any non-message type, malformed
// body, or missing/invalid event_id so the caller can drop it — a frame
// without a usable ordering key cannot be placed in the scrollback, and
// the AsyncAPI contract makes event_id required, so its absence is a
// server-side fault the client fails closed on. The Unix-ms timestamp is
// converted to a time.Time here; a 0 timestamp yields a zero time.Time.
func DecodeChatMessage(raw []byte) (ChatMessageReceived, bool) {
	var f MessageFrame
	if err := json.Unmarshal(raw, &f); err != nil || f.Type != "message" {
		return ChatMessageReceived{}, false
	}
	eventID, err := timeline.ParseEventID(f.EventID)
	if err != nil {
		return ChatMessageReceived{}, false
	}
	return ChatMessageReceived{
		RoomID:  f.RoomID,
		Sender:  f.Sender,
		Text:    f.Text,
		EventID: eventID,
		At:      millisToTime(f.Timestamp),
	}, true
}

// DecodeMemberEvent parses a raw inbound frame as a {"type":"joined"} or
// {"type":"left"} membership frame, normalising the two into a single
// MemberEventReceived. It returns ok=false for any other type, malformed
// body, or missing/invalid event_id — the same fail-closed rule as
// DecodeChatMessage, since a system line also needs a valid ordering key.
func DecodeMemberEvent(raw []byte) (MemberEventReceived, bool) {
	var f MemberEventFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return MemberEventReceived{}, false
	}
	var kind MemberEventKind
	switch f.Type {
	case "joined":
		kind = MemberJoined
	case "left":
		kind = MemberLeft
	default:
		return MemberEventReceived{}, false
	}
	eventID, err := timeline.ParseEventID(f.EventID)
	if err != nil {
		return MemberEventReceived{}, false
	}
	return MemberEventReceived{
		RoomID:  f.RoomID,
		User:    f.User,
		Kind:    kind,
		EventID: eventID,
		At:      millisToTime(f.Timestamp),
	}, true
}

// DecodeErrorFrame parses a raw inbound frame as a {"type":"error"}
// frame. It returns ok=false for any non-error type or malformed body.
// The numeric code is intentionally dropped — the client keys off the
// status string (see errmap.go), never the code.
func DecodeErrorFrame(raw []byte) (ErrorFrameReceived, bool) {
	var f struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || f.Type != "error" {
		return ErrorFrameReceived{}, false
	}
	return ErrorFrameReceived{Status: f.Status, Message: f.Message}, true
}

// millisToTime converts a Unix-millisecond timestamp into a time.Time,
// mapping 0 (the server's zero-value sentinel) to a zero time.Time so
// downstream renderers can show a placeholder instead of the epoch.
func millisToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

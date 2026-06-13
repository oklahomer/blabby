package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// DefaultRoomCallTimeout bounds every request issued by the three
// room-endpoint Cmds. Matches DefaultLoginTimeout's 5s — these calls
// are user-blocking and we prefer a short, predictable ceiling over a
// long indefinite wait that would freeze the UI.
const DefaultRoomCallTimeout = 5 * time.Second

// JoinedRoomsLoaded is emitted by LoadJoinedRoomsCmd on HTTP 200. The
// slice is exactly the room_ids the server returned, in order.
type JoinedRoomsLoaded struct {
	RoomIDs []string
}

// JoinedRoomsLoadFailed is emitted by LoadJoinedRoomsCmd for every
// non-success outcome — error envelopes, malformed bodies, network
// failures. Status is empty for transport errors and for bodies the
// server returned without a parseable envelope. HTTPStatus is 0 for
// transport failures and the response's status otherwise.
type JoinedRoomsLoadFailed struct {
	Status     string
	Message    string
	HTTPStatus int
}

// RoomsLoaded is emitted by LoadRoomsCmd on HTTP 200 with the full
// server catalogue. Order matches the response body verbatim.
type RoomsLoaded struct {
	Rooms []Room
}

// RoomsLoadFailed is emitted by LoadRoomsCmd for every non-success
// outcome. Fields follow the same convention as JoinedRoomsLoadFailed.
type RoomsLoadFailed struct {
	Status     string
	Message    string
	HTTPStatus int
}

// RoomJoined is emitted by JoinRoomCmd on HTTP 200. RoomName is
// captured at dispatch time from the search-modal row the user
// selected — the success response body itself does not echo the name,
// so we propagate it through the Cmd so downstream code can render
// without a second round-trip.
type RoomJoined struct {
	RoomID   string
	RoomName string
}

// RoomJoinFailed is emitted by JoinRoomCmd for every non-success
// outcome. RoomID is echoed back so the modal can render
// "Already joined {X}" without re-deriving the row that failed.
type RoomJoinFailed struct {
	RoomID     string
	Status     string
	Message    string
	HTTPStatus int
}

// LoadJoinedRoomsCmd performs GET {server}/rooms/joined with the
// bearer header and emits exactly one outbound tea.Msg describing the
// outcome. The token never appears outside the Authorization header.
func LoadJoinedRoomsCmd(client *http.Client, server, token string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		raw, httpStatus, err := doRoomRequest(client, http.MethodGet, server, "/rooms/joined", token, nil, timeout)
		if err != nil {
			return JoinedRoomsLoadFailed{Message: err.Error()}
		}
		if httpStatus == http.StatusOK {
			var resp JoinedRoomsResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return JoinedRoomsLoadFailed{
					Message:    fmt.Sprintf("decode joined rooms: %s", err.Error()),
					HTTPStatus: httpStatus,
				}
			}
			return JoinedRoomsLoaded(resp)
		}
		status, message := parseErrorEnvelope(raw, httpStatus)
		return JoinedRoomsLoadFailed{
			Status:     status,
			Message:    message,
			HTTPStatus: httpStatus,
		}
	}
}

// LoadRoomsCmd performs GET {server}/rooms with the bearer header and
// emits exactly one outbound tea.Msg describing the outcome. The token
// never appears outside the Authorization header.
func LoadRoomsCmd(client *http.Client, server, token string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		raw, httpStatus, err := doRoomRequest(client, http.MethodGet, server, "/rooms", token, nil, timeout)
		if err != nil {
			return RoomsLoadFailed{Message: err.Error()}
		}
		if httpStatus == http.StatusOK {
			var resp RoomListResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return RoomsLoadFailed{
					Message:    fmt.Sprintf("decode room list: %s", err.Error()),
					HTTPStatus: httpStatus,
				}
			}
			return RoomsLoaded(resp)
		}
		status, message := parseErrorEnvelope(raw, httpStatus)
		return RoomsLoadFailed{
			Status:     status,
			Message:    message,
			HTTPStatus: httpStatus,
		}
	}
}

// JoinRoomCmd performs PUT {server}/rooms/{roomID}/membership with the
// bearer header and emits exactly one outbound tea.Msg describing the
// outcome. roomName is echoed back inside RoomJoined so the modal can
// render the friendly name without re-deriving it from the server's
// catalogue. The membership resource needs no request body.
func JoinRoomCmd(client *http.Client, server, token, roomID, roomName string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		path := "/rooms/" + url.PathEscape(roomID) + "/membership"
		raw, httpStatus, err := doRoomRequest(client, http.MethodPut, server, path, token, nil, timeout)
		if err != nil {
			return RoomJoinFailed{RoomID: roomID, Message: err.Error()}
		}
		if httpStatus == http.StatusOK {
			var resp JoinSuccessResponse
			if err := json.Unmarshal(raw, &resp); err != nil || !resp.Success {
				return RoomJoinFailed{
					RoomID:     roomID,
					Message:    "server reported join with no success flag",
					HTTPStatus: httpStatus,
				}
			}
			return RoomJoined{RoomID: roomID, RoomName: roomName}
		}
		status, message := parseErrorEnvelope(raw, httpStatus)
		return RoomJoinFailed{
			RoomID:     roomID,
			Status:     status,
			Message:    message,
			HTTPStatus: httpStatus,
		}
	}
}

// doRoomRequest is the shared transport for the three room Cmds. It
// builds the request, attaches the bearer header (always; body content
// type is attached when body is non-nil), reads up to defaultReadLimitBytes
// of the response, and returns (raw body, http status, err). A non-nil
// err signals a network-level failure — the caller emits the *Failed
// variant with HTTPStatus=0.
func doRoomRequest(client *http.Client, method, server, path, token string, body []byte, timeout time.Duration) ([]byte, int, error) {
	if timeout <= 0 {
		timeout = DefaultRoomCallTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	endpoint := strings.TrimRight(server, "/") + path
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, defaultReadLimitBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read %s %s response: %w", method, path, err)
	}
	return raw, resp.StatusCode, nil
}

// parseErrorEnvelope decodes a non-2xx response body. When the server
// returned a parseable envelope, the returned status and message come
// straight from it. When the body is empty or not an envelope, status
// is "" and message is a generic "server returned 502 Bad Gateway"
// fallback derived from the HTTP status. The fallback message never
// echoes the request body or any header.
func parseErrorEnvelope(raw []byte, httpStatus int) (string, string) {
	var env ErrorEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Error.Status != "" {
		return env.Error.Status, env.Error.Message
	}
	return "", fmt.Sprintf("server returned %d %s", httpStatus, http.StatusText(httpStatus))
}

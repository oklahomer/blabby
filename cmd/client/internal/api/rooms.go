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

// JoinedRoomsLoaded is emitted by LoadJoinedRoomsCmd on HTTP 200. The slice is
// the joined rooms the server returned, in order, as descriptors (opaque R… id +
// name) so names survive a reload without an in-session lookup.
type JoinedRoomsLoaded struct {
	Rooms      []Room
	Generation SessionGeneration
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
	Generation SessionGeneration
}

// RoomQuery filters and paginates a LoadRoomsCmd catalogue request. The zero
// value asks for the unfiltered first page. Query maps to the server's `q`
// (room-name substring) parameter; After to its keyset cursor.
type RoomQuery struct {
	Query string
	After string
}

// RoomsLoaded is emitted by LoadRoomsCmd on HTTP 200 with one page of the
// server catalogue. Order matches the response body verbatim. Next is the
// `after` value for the following page ("" when the listing is exhausted).
// Query and After echo the dispatched RoomQuery so the search modal can drop
// results that no longer match what the user has typed since, and distinguish
// a fresh page (After == "") from an appended one.
type RoomsLoaded struct {
	Rooms      []Room
	Next       string
	Query      string
	After      string
	Generation SessionGeneration
}

// RoomsLoadFailed is emitted by LoadRoomsCmd for every non-success
// outcome. Fields follow the same convention as JoinedRoomsLoadFailed.
type RoomsLoadFailed struct {
	Status     string
	Message    string
	HTTPStatus int
	Query      string
	After      string
	Generation SessionGeneration
}

// RoomJoined is emitted by JoinRoomCmd on HTTP 200. RoomName is
// captured at dispatch time from the search-modal row the user
// selected — the success response body itself does not echo the name,
// so we propagate it through the Cmd so downstream code can render
// without a second round-trip.
type RoomJoined struct {
	RoomID     string
	RoomName   string
	Generation SessionGeneration
}

// RoomJoinFailed is emitted by JoinRoomCmd for every non-success
// outcome. RoomID is echoed back so the modal can render
// "Already joined {X}" without re-deriving the row that failed.
type RoomJoinFailed struct {
	RoomID     string
	Status     string
	Message    string
	HTTPStatus int
	Generation SessionGeneration
}

// LoadJoinedRoomsCmd performs GET {server}/rooms/joined with the
// bearer header and emits exactly one outbound tea.Msg describing the
// outcome. The token never appears outside the Authorization header.
func LoadJoinedRoomsCmd(client *http.Client, server, token string, generation SessionGeneration, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		raw, httpStatus, err := doRoomRequest(client, http.MethodGet, server, "/rooms/joined", token, nil, timeout)
		if err != nil {
			return JoinedRoomsLoadFailed{Message: err.Error(), Generation: generation}
		}
		if httpStatus == http.StatusOK {
			var resp RoomListResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return JoinedRoomsLoadFailed{
					Message:    fmt.Sprintf("decode joined rooms: %s", err.Error()),
					HTTPStatus: httpStatus,
					Generation: generation,
				}
			}
			return JoinedRoomsLoaded{Rooms: resp.Rooms, Generation: generation}
		}
		status, message := parseErrorEnvelope(raw, httpStatus)
		return JoinedRoomsLoadFailed{
			Status:     status,
			Message:    message,
			HTTPStatus: httpStatus,
			Generation: generation,
		}
	}
}

// LoadRoomsCmd performs GET {server}/rooms with the bearer header and emits
// exactly one outbound tea.Msg describing the outcome. query narrows and
// pages the catalogue via the server's q/after parameters. The token never
// appears outside the Authorization header.
func LoadRoomsCmd(client *http.Client, server, token string, query RoomQuery, generation SessionGeneration, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		path := "/rooms"
		params := url.Values{}
		if query.Query != "" {
			params.Set("q", query.Query)
		}
		if query.After != "" {
			params.Set("after", query.After)
		}
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		raw, httpStatus, err := doRoomRequest(client, http.MethodGet, server, path, token, nil, timeout)
		if err != nil {
			return RoomsLoadFailed{Message: err.Error(), Query: query.Query, After: query.After, Generation: generation}
		}
		if httpStatus == http.StatusOK {
			var resp RoomListResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return RoomsLoadFailed{
					Message:    fmt.Sprintf("decode room list: %s", err.Error()),
					HTTPStatus: httpStatus,
					Query:      query.Query,
					After:      query.After,
					Generation: generation,
				}
			}
			next := ""
			if resp.Next != nil {
				next = *resp.Next
			}
			return RoomsLoaded{Rooms: resp.Rooms, Next: next, Query: query.Query, After: query.After, Generation: generation}
		}
		status, message := parseErrorEnvelope(raw, httpStatus)
		return RoomsLoadFailed{
			Status:     status,
			Message:    message,
			HTTPStatus: httpStatus,
			Query:      query.Query,
			After:      query.After,
			Generation: generation,
		}
	}
}

// JoinRoomCmd performs PUT {server}/rooms/{roomID}/membership with the
// bearer header and emits exactly one outbound tea.Msg describing the
// outcome. roomName is echoed back inside RoomJoined so the modal can
// render the friendly name without re-deriving it from the server's
// catalogue. The membership resource needs no request body.
func JoinRoomCmd(client *http.Client, server, token, roomID, roomName string, generation SessionGeneration, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		path := "/rooms/" + url.PathEscape(roomID) + "/membership"
		raw, httpStatus, err := doRoomRequest(client, http.MethodPut, server, path, token, nil, timeout)
		if err != nil {
			return RoomJoinFailed{RoomID: roomID, Message: err.Error(), Generation: generation}
		}
		if httpStatus == http.StatusOK {
			var resp JoinSuccessResponse
			if err := json.Unmarshal(raw, &resp); err != nil || !resp.Success {
				return RoomJoinFailed{
					RoomID:     roomID,
					Message:    "server reported join with no success flag",
					HTTPStatus: httpStatus,
					Generation: generation,
				}
			}
			return RoomJoined{RoomID: roomID, RoomName: roomName, Generation: generation}
		}
		status, message := parseErrorEnvelope(raw, httpStatus)
		return RoomJoinFailed{
			RoomID:     roomID,
			Status:     status,
			Message:    message,
			HTTPStatus: httpStatus,
			Generation: generation,
		}
	}
}

// RoomCreated is emitted by CreateRoomCmd on HTTP 201 with the new room's
// descriptor. The server has already seeded the caller as the room's owner,
// so downstream code treats this like a completed join.
type RoomCreated struct {
	Room       Room
	Generation SessionGeneration
}

// RoomCreateFailed is emitted by CreateRoomCmd for every non-success outcome.
// Fields follow the same convention as RoomsLoadFailed.
type RoomCreateFailed struct {
	Status     string
	Message    string
	HTTPStatus int
	Generation SessionGeneration
}

// RoomLeft is emitted by LeaveRoomCmd on HTTP 200. RoomName is captured at
// dispatch time, like RoomJoined, so downstream copy can name the room.
type RoomLeft struct {
	RoomID     string
	RoomName   string
	Generation SessionGeneration
}

// RoomLeaveFailed is emitted by LeaveRoomCmd for every non-success outcome.
// RoomID is echoed back so the pane can render the failure against the row
// that produced it.
type RoomLeaveFailed struct {
	RoomID     string
	Status     string
	Message    string
	HTTPStatus int
	Generation SessionGeneration
}

// CreateRoomCmd performs POST {server}/rooms with the bearer header and emits
// exactly one outbound tea.Msg describing the outcome.
func CreateRoomCmd(client *http.Client, server, token, name string, generation SessionGeneration, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		body, err := json.Marshal(CreateRoomRequest{Name: name})
		if err != nil {
			return RoomCreateFailed{Message: fmt.Sprintf("encode create-room request: %s", err.Error()), Generation: generation}
		}
		raw, httpStatus, err := doRoomRequest(client, http.MethodPost, server, "/rooms", token, body, timeout)
		if err != nil {
			return RoomCreateFailed{Message: err.Error(), Generation: generation}
		}
		if httpStatus == http.StatusCreated {
			var room Room
			if err := json.Unmarshal(raw, &room); err != nil || room.ID == "" {
				return RoomCreateFailed{
					Message:    "server reported creation with no room descriptor",
					HTTPStatus: httpStatus,
					Generation: generation,
				}
			}
			return RoomCreated{Room: room, Generation: generation}
		}
		status, message := parseErrorEnvelope(raw, httpStatus)
		return RoomCreateFailed{
			Status:     status,
			Message:    message,
			HTTPStatus: httpStatus,
			Generation: generation,
		}
	}
}

// LeaveRoomCmd performs DELETE {server}/rooms/{roomID}/membership with the
// bearer header and emits exactly one outbound tea.Msg describing the
// outcome. roomName is echoed back inside RoomLeft, mirroring JoinRoomCmd.
func LeaveRoomCmd(client *http.Client, server, token, roomID, roomName string, generation SessionGeneration, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		path := "/rooms/" + url.PathEscape(roomID) + "/membership"
		raw, httpStatus, err := doRoomRequest(client, http.MethodDelete, server, path, token, nil, timeout)
		if err != nil {
			return RoomLeaveFailed{RoomID: roomID, Message: err.Error(), Generation: generation}
		}
		if httpStatus == http.StatusOK {
			var resp JoinSuccessResponse
			if err := json.Unmarshal(raw, &resp); err != nil || !resp.Success {
				return RoomLeaveFailed{
					RoomID:     roomID,
					Message:    "server reported leave with no success flag",
					HTTPStatus: httpStatus,
					Generation: generation,
				}
			}
			return RoomLeft{RoomID: roomID, RoomName: roomName, Generation: generation}
		}
		status, message := parseErrorEnvelope(raw, httpStatus)
		return RoomLeaveFailed{
			RoomID:     roomID,
			Status:     status,
			Message:    message,
			HTTPStatus: httpStatus,
			Generation: generation,
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

// parseErrorEnvelope decodes a non-2xx response body using the shared gateway
// error-envelope parser, deriving the fallback text from the numeric HTTP status
// that room commands already carry.
func parseErrorEnvelope(raw []byte, httpStatus int) (string, string) {
	return decodeErrorEnvelope(raw, fmt.Sprintf("%d %s", httpStatus, http.StatusText(httpStatus)))
}

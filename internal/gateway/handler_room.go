package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/roomrepo"
)

const (
	maxRoomMessageBodyBytes = 8 * 1024 // 8 KiB request body cap
	maxRoomMessageTextBytes = 4 * 1024 // 4 KiB text cap
)

// Endpoint labels double as both the mux pattern in RegisterRoutes and
// the structured-log endpoint field. Defining them once keeps the route
// table and log lines from drifting apart on rename.
const (
	endpointRoomList             = "GET /rooms"
	endpointRoomJoined           = "GET /rooms/joined"
	endpointRoomMembershipPut    = "PUT /rooms/{id}/membership"
	endpointRoomMembershipDelete = "DELETE /rooms/{id}/membership"
	endpointRoomMessage          = "POST /rooms/{id}/messages"
)

// Outcome labels for the structured exit log on every room handler.
const (
	outcomeOK             = "ok"
	outcomeBusinessError  = "business_error"
	outcomeTransportError = "transport_error"
)

type sendMessageRequest struct {
	Text string `json:"text"`
}

type successResponse struct {
	Success bool `json:"success"`
}

type sendMessageSuccessResponse struct {
	Success   bool  `json:"success"`
	Timestamp int64 `json:"timestamp"`
}

// handleRoomMembershipPut ensures that the authenticated user is a member of
// the selected room. The User grain adapts Room's action-oriented Join result:
// ROOM_ALREADY_MEMBER confirms the PUT state and repairs its local projection,
// while other business errors still reach this handler.
//
// Note: Go 1.22+ mux dispatches PUT /rooms//membership to the catch-all "/"
// pattern (handleNotFound), so the empty-segment case never reaches this
// handler. A malformed segment (e.g. PUT /rooms/%20/membership), which the mux
// does match, fails id.ParseRoomCode and is rejected as a bad request.
func (g *Gateway) handleRoomMembershipPut(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomMembershipPut)
	if !ok {
		return
	}
	roomID, ok := g.requireRoomID(w, r, endpointRoomMembershipPut, userID)
	if !ok {
		return
	}
	logRoomEntry(endpointRoomMembershipPut, r.Method, userID, roomID)
	resp, err := g.userGrainFor(userID).JoinRoom(&userpb.JoinRoomRequest{RoomId: roomID.String()})
	if err != nil {
		logRoomTransportError(endpointRoomMembershipPut, r.Method, userID, roomID)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to reach user grain"))
		return
	}
	if pe := resp.GetError(); pe != nil {
		writeBusinessErrorResponse(w, endpointRoomMembershipPut, r.Method, userID, roomID, pe)
		return
	}
	logRoomExit(endpointRoomMembershipPut, r.Method, userID, roomID, outcomeOK, 0)
	writeJSON(w, http.StatusOK, successResponse{Success: true})
}

// handleRoomMembershipDelete ensures that the authenticated user is not a
// member of the selected room. The User grain similarly treats
// ROOM_NOT_MEMBER as confirmation of the DELETE state and reconciles its local
// joined-room projection before returning success.
func (g *Gateway) handleRoomMembershipDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomMembershipDelete)
	if !ok {
		return
	}
	roomID, ok := g.requireRoomID(w, r, endpointRoomMembershipDelete, userID)
	if !ok {
		return
	}
	logRoomEntry(endpointRoomMembershipDelete, r.Method, userID, roomID)
	resp, err := g.userGrainFor(userID).LeaveRoom(&userpb.LeaveRoomRequest{RoomId: roomID.String()})
	if err != nil {
		logRoomTransportError(endpointRoomMembershipDelete, r.Method, userID, roomID)
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to reach user grain"))
		return
	}
	if pe := resp.GetError(); pe != nil {
		writeBusinessErrorResponse(w, endpointRoomMembershipDelete, r.Method, userID, roomID, pe)
		return
	}
	logRoomExit(endpointRoomMembershipDelete, r.Method, userID, roomID, outcomeOK, 0)
	writeJSON(w, http.StatusOK, successResponse{Success: true})
}

// handleRoomSendMessage dispatches POST /rooms/{id}/messages to the
// user's grain. The request body is JSON `{"text":"..."}` capped at
// maxRoomMessageBodyBytes; the text payload is capped at
// maxRoomMessageTextBytes. The returned timestamp is converted to int64
// Unix milliseconds at this boundary.
func (g *Gateway) handleRoomSendMessage(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticatedUserID(w, r, endpointRoomMessage)
	if !ok {
		return
	}
	roomID, ok := g.requireRoomID(w, r, endpointRoomMessage, userID)
	if !ok {
		return
	}
	req, derr := decodeSendMessageRequest(r, w)
	if derr != nil {
		slog.Warn("room handler rejected request",
			"endpoint", endpointRoomMessage, "method", r.Method,
			"user_id", userID, "room_id", roomID, "reason", derr.reason)
		WriteErrorResponse(w, httpStatus(derr.detail.Code), derr.detail)
		return
	}

	slog.Info("room handler enter",
		"endpoint", endpointRoomMessage, "method", r.Method,
		"user_id", userID, "room_id", roomID, "text_len", len(req.Text))

	resp, err := g.userGrainFor(userID).SendMessage(&userpb.SendMessageRequest{
		RoomId: roomID.String(), Text: req.Text,
	})
	if err != nil {
		slog.Warn("room handler transport error",
			"endpoint", endpointRoomMessage, "method", r.Method,
			"user_id", userID, "room_id", roomID,
			"outcome", outcomeTransportError, "text_len", len(req.Text))
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to reach user grain"))
		return
	}
	if pe := resp.GetError(); pe != nil {
		ed, parseErr := FromProtoErrorDetail(pe)
		if parseErr != nil {
			slog.Error("room handler received invalid business error",
				"endpoint", endpointRoomMessage, "method", r.Method,
				"user_id", userID, "room_id", roomID,
				"code", pe.GetCode(), "status", pe.GetStatus(),
				"error", parseErr, "text_len", len(req.Text))
			WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("internal server error"))
			return
		}
		slog.Info("room handler exit",
			"endpoint", endpointRoomMessage, "method", r.Method,
			"user_id", userID, "room_id", roomID,
			"outcome", outcomeBusinessError, "code", ed.Code, "text_len", len(req.Text))
		WriteErrorResponse(w, httpStatus(ed.Code), ed)
		return
	}

	var ts int64
	if pt := resp.GetTimestamp(); pt != nil {
		ts = pt.AsTime().UnixMilli()
	}
	slog.Info("room handler exit",
		"endpoint", endpointRoomMessage, "method", r.Method,
		"user_id", userID, "room_id", roomID,
		"outcome", outcomeOK, "code", 0, "text_len", len(req.Text))
	writeJSON(w, http.StatusOK, sendMessageSuccessResponse{Success: true, Timestamp: ts})
}

// authenticatedUserID extracts the user ID from the request context that
// authMiddleware has populated. Behind the middleware the boolean is
// always true; the defensive 5001 path covers tests that invoke the
// handler directly without the middleware.
func authenticatedUserID(w http.ResponseWriter, r *http.Request, endpoint string) (id.UserID, bool) {
	userID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		slog.Error("auth context missing on protected route",
			"endpoint", endpoint, "method", r.Method)
		WriteErrorResponse(w, http.StatusInternalServerError,
			ErrInternalError("authentication context unavailable"))
		return id.UserID{}, false
	}
	return userID, true
}

// requireRoomID extracts {id} from the request path as a client-facing R… code
// and resolves it to the internal RoomID via the room directory. A malformed code
// is a 400; a code that resolves to no active room is a 404; a directory failure
// is a 503. The internal id is never accepted from or returned to the client.
func (g *Gateway) requireRoomID(w http.ResponseWriter, r *http.Request, endpoint string, userID id.UserID) (id.RoomID, bool) {
	code, err := id.ParseRoomCode(r.PathValue("id"))
	if err != nil {
		slog.Warn("room handler rejected request",
			"endpoint", endpoint, "method", r.Method,
			"user_id", userID, "reason", "invalid_room_code")
		WriteErrorResponse(w, http.StatusBadRequest, ErrInvalidRequest("room id is invalid"))
		return id.RoomID{}, false
	}
	roomID, err := g.rooms.Resolve(r.Context(), code)
	switch {
	case errors.Is(err, roomrepo.ErrRoomNotFound):
		slog.Warn("room handler rejected request",
			"endpoint", endpoint, "method", r.Method,
			"user_id", userID, "reason", "room_not_found")
		WriteErrorResponse(w, http.StatusNotFound, ErrRoomNotFound("room not found"))
		return id.RoomID{}, false
	case err != nil:
		logRoomTransportError(endpoint, r.Method, userID, id.RoomID{})
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("failed to resolve room"))
		return id.RoomID{}, false
	}
	return roomID, true
}

// roomBodyError carries both the user-facing gateway ErrorDetail and a
// coarse classifier for the structured-log "reason" field. It is internal
// to this file; decodeSendMessageRequest is the only producer.
//
// The classifier is distinct from the canonical status string so
// operators can grep logs by cause ("malformed_body" vs "trailing_garbage"
// vs "empty_text" etc.) — the status string alone collapses every 400
// to "INVALID_REQUEST".
type roomBodyError struct {
	reason string
	detail ErrorDetail
}

func (e *roomBodyError) Error() string { return e.detail.Message }

// decodeSendMessageRequest parses and validates the POST body for
// handleRoomSendMessage. It returns the parsed request on success, or a
// roomBodyError describing the rejection cause on failure.
//
// The MaxBytesReader is installed on r.Body before decoding so an
// oversize body surfaces as *http.MaxBytesError at decode time and is
// mapped to a "payload_too_large" / 413 response.
func decodeSendMessageRequest(r *http.Request, w http.ResponseWriter) (*sendMessageRequest, *roomBodyError) {
	if !contentTypeIsJSON(r.Header.Get("Content-Type")) {
		return nil, &roomBodyError{
			reason: "content_type",
			detail: ErrInvalidRequest("content-type must be application/json"),
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRoomMessageBodyBytes)

	var req sendMessageRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, &roomBodyError{
				reason: "payload_too_large",
				detail: ErrPayloadTooLarge("request body exceeds maximum size"),
			}
		}
		return nil, &roomBodyError{
			reason: "malformed_body",
			detail: ErrInvalidRequest("malformed request body"),
		}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, &roomBodyError{
			reason: "trailing_garbage",
			detail: ErrInvalidRequest("malformed request body"),
		}
	}
	if strings.TrimSpace(req.Text) == "" {
		return nil, &roomBodyError{
			reason: "empty_text",
			detail: ErrInvalidRequest("text is required"),
		}
	}
	if len(req.Text) > maxRoomMessageTextBytes {
		return nil, &roomBodyError{
			reason: "text_too_long",
			detail: ErrInvalidRequest("text exceeds maximum length"),
		}
	}
	return &req, nil
}

// contentTypeIsJSON parses the Content-Type header and returns true if
// the media type is application/json. Charset variants are accepted.
func contentTypeIsJSON(header string) bool {
	if header == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}
	return mediaType == "application/json"
}

// writeJSON writes v as a JSON body with the given HTTP status. Used by
// the success path of every room command handler.
func writeJSON(w http.ResponseWriter, httpStatus int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to write room handler response", "error", err)
	}
}

// writeBusinessErrorResponse converts a non-nil grain ErrorDetail into
// the gateway envelope and writes it with the mapped HTTP status. The
// matching exit log line is emitted before the write so it appears in
// chronological order with the entry log.
//
// Callers MUST pass a non-nil pe; every call site already checks
// `resp.GetError() != nil` before invoking this helper.
func writeBusinessErrorResponse(w http.ResponseWriter, endpoint, method string, userID id.UserID, roomID id.RoomID, pe *commonpb.ErrorDetail) {
	ed, err := FromProtoErrorDetail(pe)
	if err != nil {
		slog.Error("room handler received invalid business error",
			"endpoint", endpoint, "method", method,
			"user_id", userID, "room_id", roomID,
			"code", pe.GetCode(), "status", pe.GetStatus(), "error", err)
		WriteErrorResponse(w, http.StatusInternalServerError, ErrInternalError("internal server error"))
		return
	}
	slog.Info("room handler exit",
		"endpoint", endpoint, "method", method,
		"user_id", userID, "room_id", roomID,
		"outcome", outcomeBusinessError, "code", ed.Code)
	WriteErrorResponse(w, httpStatus(ed.Code), ed)
}

func logRoomEntry(endpoint, method string, userID id.UserID, roomID id.RoomID) {
	slog.Info("room handler enter",
		"endpoint", endpoint, "method", method,
		"user_id", userID, "room_id", roomID)
}

func logRoomExit(endpoint, method string, userID id.UserID, roomID id.RoomID, outcome string, code int) {
	slog.Info("room handler exit",
		"endpoint", endpoint, "method", method,
		"user_id", userID, "room_id", roomID,
		"outcome", outcome, "code", code)
}

func logRoomTransportError(endpoint, method string, userID id.UserID, roomID id.RoomID) {
	slog.Warn("room handler transport error",
		"endpoint", endpoint, "method", method,
		"user_id", userID, "room_id", roomID,
		"outcome", outcomeTransportError)
}

// Package api owns the HTTP + WebSocket transport between the TUI
// client and the blabby server. It defines the typed wire schema, the
// tea.Cmd-shaped operations the root Model dispatches (LoginCmd,
// DialAndAuthCmd, ReadLoopCmd), and small helpers for translating
// server URLs and decoding JWT payloads.
//
// The wire types deliberately duplicate the server's gateway and
// connection schemas. The gateway is the project's JSON boundary;
// inverting the dependency to share a types package would couple the
// client to server-internal packages. Drift is caught by the
// integration test that exercises the real server through
// httptest.Server.
package api

// LoginRequest mirrors internal/gateway/handler.go LoginRequest. Login identity
// is the account email; the server treats both fields as required and trims
// whitespace before authenticating.
type LoginRequest struct {
	MailAddress string `json:"mail_address"`
	Password    string `json:"password"`
}

// LoginResponse mirrors internal/gateway/handler.go LoginResponse for
// HTTP 200 outcomes.
type LoginResponse struct {
	Token string `json:"token"`
}

// RegisterRequest mirrors internal/gateway/handler_register.go RegisterRequest.
type RegisterRequest struct {
	MailAddress string `json:"mail_address"`
	Handle      string `json:"handle"`
	Password    string `json:"password"`
}

// VerifyRequest mirrors internal/gateway/handler_verify.go VerifyRequest.
type VerifyRequest struct {
	MailAddress string `json:"mail_address"`
	PIN         string `json:"pin"`
}

// ResendVerificationRequest mirrors internal/gateway/handler_verify.go
// ResendVerificationRequest.
type ResendVerificationRequest struct {
	MailAddress string `json:"mail_address"`
}

// ErrorDetail mirrors internal/gateway/errors.go ErrorDetail. JSON numbers are
// decoded into int here because the client does not need the server's internal
// errcode.Code type.
type ErrorDetail struct {
	Code    int    `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ErrorEnvelope mirrors internal/gateway/errors.go ErrorResponse. All
// HTTP error paths from the gateway wrap their ErrorDetail in this
// envelope.
type ErrorEnvelope struct {
	Error ErrorDetail `json:"error"`
}

// AuthFrame is the outbound first WebSocket frame the client sends
// after the upgrade succeeds — mirrors the server's
// internal/actor/connection/decoder.go auth wire shape.
type AuthFrame struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

// FrameEnvelope is the minimal inbound frame shape used to dispatch
// server messages by Type. Higher layers decode the full payload
// once they see a Type they care about.
type FrameEnvelope struct {
	Type string `json:"type"`
}

// PongFrame is the outbound reply to a server "ping" frame — mirrors
// the pong wire shape the server decodes in
// internal/actor/connection/decoder.go.
type PongFrame struct {
	Type string `json:"type"`
}

// AuthErrorFrame mirrors internal/actor/connection/encoder.go's
// auth_error payload (1001/1002/1003).
type AuthErrorFrame struct {
	Type    string `json:"type"`
	Code    int    `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Room mirrors internal/gateway/handler_room_query.go roomDescriptor —
// one entry in the catalogue returned by GET /rooms.
type Room struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RoomListResponse mirrors internal/gateway/handler_room_query.go
// roomCataloguePage / roomListResponse for HTTP 200 outcomes of GET /rooms and
// GET /rooms/joined: both return room descriptors (opaque R… id + name). Next
// is the catalogue's continuation cursor — the `after` value for the next
// page, nil when the listing is exhausted (and always nil for /rooms/joined,
// which is unpaginated).
type RoomListResponse struct {
	Rooms []Room  `json:"rooms"`
	Next  *string `json:"next"`
}

// CreateRoomRequest mirrors internal/gateway/handler_room_create.go
// createRoomRequest.
type CreateRoomRequest struct {
	Name string `json:"name"`
}

// JoinSuccessResponse mirrors internal/gateway/handler_room.go
// successResponse for the membership PUT's 200 outcome. The server emits
// the same shape from DELETE and other room-mutation endpoints; the type is
// named for the join site that consumes it today.
type JoinSuccessResponse struct {
	Success bool `json:"success"`
}

// SendMessageRequestBody mirrors internal/gateway/handler_room.go
// sendMessageRequest — the JSON body of POST /rooms/{id}/messages.
type SendMessageRequestBody struct {
	Text string `json:"text"`
}

// SendMessageResponse mirrors internal/gateway/handler_room.go
// sendMessageSuccessResponse for the 200 outcome. Timestamp is the
// server-assigned post time in Unix milliseconds; SendMessageCmd
// parses it into a time.Time at the boundary.
type SendMessageResponse struct {
	Success   bool  `json:"success"`
	Timestamp int64 `json:"timestamp"`
}

// UserRef mirrors the server's connection.UserRef — the nested
// {"id","name"} object the message and room-event frames carry. It is a
// plain wire value; the server already validated the identity it holds.
type UserRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MessageFrame mirrors internal/actor/connection/encoder.go
// encodeMessage — the inbound {"type":"message"} chat frame the Room
// grain fans out to every member (the sender included). The sender is a
// nested {"id","name"} object. EventID is the message's timeline event
// id as a decimal Snowflake string; it orders and dedups the frame
// against the backfilled history. Timestamp is Unix milliseconds; the
// server emits 0 for a zero-value time.
type MessageFrame struct {
	Type      string  `json:"type"`
	RoomID    string  `json:"room_id"`
	EventID   string  `json:"event_id"`
	Sender    UserRef `json:"sender"`
	Text      string  `json:"text"`
	Timestamp int64   `json:"timestamp"`
}

// MemberEventFrame mirrors internal/actor/connection/encoder.go
// encodeMember — the shared shape of the inbound {"type":"joined"} and
// {"type":"left"} membership frames. EventID is the membership event's
// timeline id as a decimal Snowflake string, with the same ordering and
// dedup role as on the message frame. User is the member the change
// applies to. Timestamp is Unix milliseconds.
type MemberEventFrame struct {
	Type      string  `json:"type"`
	RoomID    string  `json:"room_id"`
	EventID   string  `json:"event_id"`
	User      UserRef `json:"user"`
	Timestamp int64   `json:"timestamp"`
}

// RoomEvent mirrors internal/gateway/handler_room_events.go roomEvent —
// one timeline entry in the GET /rooms/{id}/events response. Type
// discriminates the shape: a "message" carries Sender+Text, while
// "member_joined"/"member_left" carry User. (The HTTP vocabulary is
// member_joined/member_left; the WebSocket frames use joined/left —
// LoadEventsCmd normalises the two into one MemberEventKind.) ID is the
// event's decimal Snowflake id; Timestamp is Unix milliseconds.
type RoomEvent struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Sender    *UserRef `json:"sender,omitempty"`
	User      *UserRef `json:"user,omitempty"`
	Text      string   `json:"text,omitempty"`
	Timestamp int64    `json:"timestamp"`
}

// RoomEventsResponse mirrors internal/gateway/handler_room_events.go
// roomEventsPage — one newest-first page of a room's timeline. Next is
// the `before` cursor for the following (older) page — the id of this
// page's last event — or null when the history is exhausted.
type RoomEventsResponse struct {
	Events []RoomEvent `json:"events"`
	Next   *string     `json:"next"`
}

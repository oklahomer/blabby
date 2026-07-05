package connection

import (
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oklahomer/blabby/internal/errcode"
)

type encodedFrame struct {
	messageType int
	data        []byte
	eventKind   string
}

func encodeOutboundMessage(msg any) (encodedFrame, bool) {
	switch m := msg.(type) {
	case *AuthSucceeded:
		return encodedFrame{messageType: websocket.TextMessage, data: encodeAuthOk(), eventKind: "auth_ok"}, true
	case *AuthFailed:
		return encodedFrame{messageType: websocket.TextMessage, data: encodeAuthError(m.Code, m.Message), eventKind: "auth_error"}, true
	case *ChatDelivered:
		return encodedFrame{
			messageType: websocket.TextMessage,
			data:        encodeMessage(m.RoomID, m.Sender, m.Text, timestampMillis(m.Timestamp), m.EventID),
			eventKind:   "message",
		}, true
	case *RoomJoined:
		return encodedFrame{
			messageType: websocket.TextMessage,
			data:        encodeMember("joined", m.RoomID, m.User, m.EventID, timestampMillis(m.At)),
			eventKind:   "event",
		}, true
	case *RoomLeft:
		return encodedFrame{
			messageType: websocket.TextMessage,
			data:        encodeMember("left", m.RoomID, m.User, m.EventID, timestampMillis(m.At)),
			eventKind:   "event",
		}, true
	case *ErrorResponse:
		return encodedFrame{messageType: websocket.TextMessage, data: encodeError(m.Code, m.Message), eventKind: "error"}, true
	case *AppPing:
		return encodedFrame{messageType: websocket.TextMessage, data: mustMarshal(map[string]any{"type": "ping"}), eventKind: "ping"}, true
	default:
		return encodedFrame{}, false
	}
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("connection: encode failed (this should be unreachable): " + err.Error())
	}
	return b
}

func encodeAuthOk() []byte {
	return mustMarshal(map[string]any{
		"type": "auth_ok",
	})
}

func encodeAuthError(code errcode.Code, message string) []byte {
	return mustMarshal(map[string]any{
		"type":    "auth_error",
		"code":    code.Int32(),
		"status":  code.Status(),
		"message": message,
	})
}

func encodeError(code errcode.Code, message string) []byte {
	return mustMarshal(map[string]any{
		"type":    "error",
		"code":    code.Int32(),
		"status":  code.Status(),
		"message": message,
	})
}

func encodeMessage(roomID string, sender UserRef, text string, timestampMs int64, eventID string) []byte {
	return mustMarshal(map[string]any{
		"type":      "message",
		"room_id":   roomID,
		"event_id":  eventID,
		"sender":    map[string]any{"id": sender.ID, "name": sender.Name},
		"text":      text,
		"timestamp": timestampMs,
	})
}

func timestampMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// encodeMember renders the shared shape of the "joined" and "left" membership
// frames: the room, the durable event id and timestamp (so the client
// interleaves the system line with messages and dedups it against backfilled
// history), and the member as a U… code + name.
func encodeMember(kind, roomID string, user UserRef, eventID string, timestampMs int64) []byte {
	return mustMarshal(map[string]any{
		"type":      kind,
		"room_id":   roomID,
		"event_id":  eventID,
		"user":      map[string]any{"id": user.ID, "name": user.Name},
		"timestamp": timestampMs,
	})
}

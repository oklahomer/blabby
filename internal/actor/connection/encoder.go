package connection

import (
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"
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
		return encodedFrame{messageType: websocket.TextMessage, data: encodeAuthError(m.Code, m.Status, m.Message), eventKind: "auth_error"}, true
	case *ChatDelivered:
		return encodedFrame{
			messageType: websocket.TextMessage,
			data:        encodeMessage(m.RoomID, m.Sender, m.Text, timestampMillis(m.Timestamp)),
			eventKind:   "message",
		}, true
	case *RoomJoined:
		return encodedFrame{messageType: websocket.TextMessage, data: encodeJoined(m.RoomID, m.User), eventKind: "event"}, true
	case *RoomLeft:
		return encodedFrame{messageType: websocket.TextMessage, data: encodeLeft(m.RoomID, m.User), eventKind: "event"}, true
	case *ErrorResponse:
		return encodedFrame{
			messageType: websocket.TextMessage,
			data: mustMarshal(map[string]any{
				"type":    "error",
				"code":    m.Code,
				"status":  m.Status,
				"message": m.Message,
			}),
			eventKind: "error",
		}, true
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

func encodeAuthError(code int32, status, message string) []byte {
	return mustMarshal(map[string]any{
		"type":    "auth_error",
		"code":    code,
		"status":  status,
		"message": message,
	})
}

func encodeMessage(roomID string, sender UserRef, text string, timestampMs int64) []byte {
	return mustMarshal(map[string]any{
		"type":      "message",
		"room_id":   roomID,
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

func encodeJoined(roomID string, user UserRef) []byte {
	return mustMarshal(map[string]any{
		"type":    "joined",
		"room_id": roomID,
		"user":    map[string]any{"id": user.ID, "name": user.Name},
	})
}

func encodeLeft(roomID string, user UserRef) []byte {
	return mustMarshal(map[string]any{
		"type":    "left",
		"room_id": roomID,
		"user":    map[string]any{"id": user.ID, "name": user.Name},
	})
}

package room

import (
	"time"

	userpb "github.com/oklahomer/blabby/gen/user"
)

// buildJoinedEvent shapes the NotifyRoomEvent payload sent to every current
// member when a user joins. roomID is the grain identity (call target is the
// recipient; UserId on the request carries the joiner — the subject).
func buildJoinedEvent(roomID, joinerID string) *userpb.NotifyRoomEventRequest {
	return &userpb.NotifyRoomEventRequest{
		RoomId:    roomID,
		UserId:    joinerID,
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED,
	}
}

// buildLeftEvent shapes the NotifyRoomEvent payload sent to every member of
// the pre-removal snapshot when a user leaves.
func buildLeftEvent(roomID, leaverID string) *userpb.NotifyRoomEventRequest {
	return &userpb.NotifyRoomEventRequest{
		RoomId:    roomID,
		UserId:    leaverID,
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT,
	}
}

// buildForwardMessage shapes the ForwardMessage payload sent to every
// current member (including the sender — multi-device echo, FR3) for a
// posted chat message. The grain works in time.Time domain values; the
// conversion to the canonical int64 Unix-milliseconds wire format happens
// here at the proto boundary.
func buildForwardMessage(roomID, senderID, text string, timestamp time.Time) *userpb.ForwardMessageRequest {
	return &userpb.ForwardMessageRequest{
		RoomId:    roomID,
		SenderId:  senderID,
		Text:      text,
		Timestamp: timestamp.UnixMilli(),
	}
}

package room

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/ids"
)

// buildJoinedEvent shapes the NotifyRoomEvent payload sent to every current
// member when a user joins. roomID is the grain identity (call target is the
// recipient; UserId on the request carries the joiner — the subject).
func buildJoinedEvent(roomID string, joinerID ids.UserID) *userpb.NotifyRoomEventRequest {
	return &userpb.NotifyRoomEventRequest{
		RoomId:    roomID,
		UserId:    joinerID.String(),
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED,
	}
}

// buildLeftEvent shapes the NotifyRoomEvent payload sent to every member of
// the pre-removal snapshot when a user leaves.
func buildLeftEvent(roomID string, leaverID ids.UserID) *userpb.NotifyRoomEventRequest {
	return &userpb.NotifyRoomEventRequest{
		RoomId:    roomID,
		UserId:    leaverID.String(),
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT,
	}
}

// buildForwardMessage shapes the ForwardMessage payload sent to every
// current member (including the sender — multi-device echo, FR3) for a
// posted chat message.
func buildForwardMessage(roomID string, senderID ids.UserID, text string, timestamp time.Time) *userpb.ForwardMessageRequest {
	return &userpb.ForwardMessageRequest{
		RoomId:    roomID,
		SenderId:  senderID.String(),
		Text:      text,
		Timestamp: timestamppb.New(timestamp),
	}
}

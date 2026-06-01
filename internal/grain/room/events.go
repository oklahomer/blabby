package room

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/id"
)

// buildJoinedEvent shapes the NotifyRoomEvent payload sent to every current
// member when a user joins. roomID is the grain identity (the call target is
// the recipient); joiner carries the subject's id and display name.
func buildJoinedEvent(roomID string, joiner id.UserRef) *userpb.NotifyRoomEventRequest {
	return &userpb.NotifyRoomEventRequest{
		RoomId:    roomID,
		User:      protoUserRef(joiner),
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED,
	}
}

// buildLeftEvent shapes the NotifyRoomEvent payload sent to every member of
// the pre-removal snapshot when a user leaves.
func buildLeftEvent(roomID string, leaver id.UserRef) *userpb.NotifyRoomEventRequest {
	return &userpb.NotifyRoomEventRequest{
		RoomId:    roomID,
		User:      protoUserRef(leaver),
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT,
	}
}

// buildForwardMessage shapes the ForwardMessage payload sent to every
// current member (including the sender — multi-device echo, FR3) for a
// posted chat message. sender carries the author's id and display name.
func buildForwardMessage(roomID string, sender id.UserRef, text string, timestamp time.Time) *userpb.ForwardMessageRequest {
	return &userpb.ForwardMessageRequest{
		RoomId:    roomID,
		Sender:    protoUserRef(sender),
		Text:      text,
		Timestamp: timestamppb.New(timestamp),
	}
}

// protoUserRef converts a domain UserRef into the wire UserRef carried by
// fan-out payloads.
func protoUserRef(u id.UserRef) *commonpb.UserRef {
	return &commonpb.UserRef{Id: u.ID().String(), Name: u.Name()}
}

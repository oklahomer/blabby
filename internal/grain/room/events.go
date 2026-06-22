package room

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

// buildJoinedEvent shapes the NotifyRoomEvent payload sent to every current
// member when a user joins. room carries the room's reference metadata (the
// connection renders its public code); joiner carries the subject's id and
// display name.
func buildJoinedEvent(room domain.RoomRef, joiner id.UserRef) *userpb.NotifyRoomEventRequest {
	return &userpb.NotifyRoomEventRequest{
		Room:      protoRoomRef(room),
		User:      protoUserRef(joiner),
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED,
	}
}

// buildLeftEvent shapes the NotifyRoomEvent payload sent to every member of
// the pre-removal snapshot when a user leaves.
func buildLeftEvent(room domain.RoomRef, leaver id.UserRef) *userpb.NotifyRoomEventRequest {
	return &userpb.NotifyRoomEventRequest{
		Room:      protoRoomRef(room),
		User:      protoUserRef(leaver),
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT,
	}
}

// buildForwardMessage shapes the ForwardMessage payload sent to every
// current member (including the sender — multi-device echo, FR3) for a
// posted chat message. room carries the room's reference metadata; sender
// carries the author's id and display name.
func buildForwardMessage(room domain.RoomRef, sender id.UserRef, text string, timestamp time.Time) *userpb.ForwardMessageRequest {
	return &userpb.ForwardMessageRequest{
		Room:      protoRoomRef(room),
		Sender:    protoUserRef(sender),
		Text:      text,
		Timestamp: timestamppb.New(timestamp),
	}
}

// protoUserRef converts the minimal user identity ref (id.UserRef: id + display
// name) into the wire UserRef carried by fan-out payloads. The richer
// public-code/status fields of common.UserRef are left empty until userrepo
// lands; see domain.UserRef for that fuller shape.
func protoUserRef(u id.UserRef) *commonpb.UserRef {
	return &commonpb.UserRef{Id: u.ID().String(), Name: u.Name()}
}

// protoRoomRef converts the grain's cached domain.RoomRef into the wire RoomRef
// carried on the JoinResponse and on fan-out (joined/left/message), so a
// downstream consumer renders the room's public code without a separate lookup.
// status is rendered as its bare label; the gateway prefixes the public code
// (R…) for clients.
func protoRoomRef(r domain.RoomRef) *commonpb.RoomRef {
	return &commonpb.RoomRef{
		RoomId:          r.ID.String(),
		PublicCode:      r.PublicCode.String(),
		Name:            r.Name,
		Status:          string(r.Status),
		MetadataVersion: r.MetadataVersion,
	}
}

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
// display name; evt carries the durable event's id and timestamp.
func buildJoinedEvent(room domain.RoomRef, joiner id.UserRef, evt MembershipEvent) *userpb.NotifyRoomEventRequest {
	req := &userpb.NotifyRoomEventRequest{
		Room:      protoRoomRef(room),
		User:      protoUserRef(joiner),
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED,
	}
	applyMembershipEvent(req, evt)
	return req
}

// buildLeftEvent shapes the NotifyRoomEvent payload sent to every member of
// the pre-removal snapshot when a user leaves.
func buildLeftEvent(room domain.RoomRef, leaver id.UserRef, evt MembershipEvent) *userpb.NotifyRoomEventRequest {
	req := &userpb.NotifyRoomEventRequest{
		Room:      protoRoomRef(room),
		User:      protoUserRef(leaver),
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT,
	}
	applyMembershipEvent(req, evt)
	return req
}

// applyMembershipEvent stamps the durable event's id and timestamp onto the
// fan-out payload. A zero event (no membership store wired) leaves the fields
// unset rather than emitting a placeholder "0" id.
func applyMembershipEvent(req *userpb.NotifyRoomEventRequest, evt MembershipEvent) {
	if evt.IsZero() {
		return
	}
	req.EventId = evt.ID.String()
	req.Timestamp = timestamppb.New(evt.OccurredAt)
}

// buildForwardMessage shapes the ForwardMessage payload sent to every
// current member (including the sender — multi-device echo, FR3) for a
// posted chat message. room carries the room's reference metadata; sender
// carries the author's id and display name; eventID is the durable
// message_posted event id ("" when the grain runs storeless in unit tests),
// so a client can order and dedup the live frame against timeline history.
func buildForwardMessage(room domain.RoomRef, sender id.UserRef, text string, timestamp time.Time, eventID string) *userpb.ForwardMessageRequest {
	return &userpb.ForwardMessageRequest{
		Room:      protoRoomRef(room),
		Sender:    protoUserRef(sender),
		Text:      text,
		Timestamp: timestamppb.New(timestamp),
		EventId:   eventID,
	}
}

// protoUserRef converts the user identity ref (id.UserRef: id + public code +
// display name) into the wire UserRef carried by fan-out payloads. The public
// code is what a connection renders as the client-facing U…; the internal id
// travels only for server-side correlation, never onto a client frame. The
// status field stays empty — fan-out consumers do not use it.
func protoUserRef(u id.UserRef) *commonpb.UserRef {
	return &commonpb.UserRef{
		Id:         u.ID().String(),
		Name:       u.Name(),
		PublicCode: u.PublicCode().String(),
	}
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

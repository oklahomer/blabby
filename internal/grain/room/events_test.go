package room

import (
	"testing"
	"time"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

// eventsRoomRef builds the room ref the fan-out builders embed, with a valid id
// and public code so the assertions can check both round-trip onto the wire.
func eventsRoomRef(t *testing.T) domain.RoomRef {
	t.Helper()
	rid, err := id.ParseRoomID("4")
	if err != nil {
		t.Fatalf("room id: %v", err)
	}
	code, err := id.ParsePublicCode("G000000004")
	if err != nil {
		t.Fatalf("public code: %v", err)
	}
	return domain.RoomRef{ID: rid, PublicCode: code, Name: "General", Status: domain.RoomStatusActive}
}

// sampleMembershipEvent builds a non-zero MembershipEvent so the fan-out
// builders carry a concrete event id and timestamp.
func sampleMembershipEvent(t *testing.T) MembershipEvent {
	t.Helper()
	eid, err := id.NewEventID(987654321)
	if err != nil {
		t.Fatalf("event id: %v", err)
	}
	return MembershipEvent{ID: eid, OccurredAt: time.UnixMilli(1_700_000_000_000)}
}

func TestBuildJoinedEvent(t *testing.T) {
	evt := sampleMembershipEvent(t)
	got := buildJoinedEvent(eventsRoomRef(t), mustUserRef(t, "1", "Alice"), evt)

	if got.GetRoom().GetRoomId() != "4" || got.GetRoom().GetPublicCode() != "G000000004" {
		t.Errorf("Room: got %+v, want id=4 code=G000000004", got.GetRoom())
	}
	if got.GetUser().GetId() != "1" {
		t.Errorf("User.Id: got %q, want %q", got.GetUser().GetId(), "1")
	}
	if got.GetUser().GetName() != "Alice" {
		t.Errorf("User.Name: got %q, want %q", got.GetUser().GetName(), "Alice")
	}
	if got.GetEventType() != userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED {
		t.Errorf("EventType: got %v, want JOINED", got.GetEventType())
	}
	if got.GetEventId() != "987654321" {
		t.Errorf("EventId: got %q, want %q", got.GetEventId(), "987654321")
	}
	if !got.GetTimestamp().AsTime().Equal(evt.OccurredAt) {
		t.Errorf("Timestamp: got %v, want %v", got.GetTimestamp().AsTime(), evt.OccurredAt)
	}
}

func TestBuildLeftEvent(t *testing.T) {
	evt := sampleMembershipEvent(t)
	got := buildLeftEvent(eventsRoomRef(t), mustUserRef(t, "1", "Alice"), evt)

	if got.GetRoom().GetRoomId() != "4" || got.GetRoom().GetPublicCode() != "G000000004" {
		t.Errorf("Room: got %+v, want id=4 code=G000000004", got.GetRoom())
	}
	if got.GetUser().GetId() != "1" {
		t.Errorf("User.Id: got %q, want %q", got.GetUser().GetId(), "1")
	}
	if got.GetUser().GetName() != "Alice" {
		t.Errorf("User.Name: got %q, want %q", got.GetUser().GetName(), "Alice")
	}
	if got.GetEventType() != userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT {
		t.Errorf("EventType: got %v, want LEFT", got.GetEventType())
	}
	if got.GetEventId() != "987654321" {
		t.Errorf("EventId: got %q, want %q", got.GetEventId(), "987654321")
	}
	if !got.GetTimestamp().AsTime().Equal(evt.OccurredAt) {
		t.Errorf("Timestamp: got %v, want %v", got.GetTimestamp().AsTime(), evt.OccurredAt)
	}
}

// TestBuildJoinedEvent_ZeroEventLeavesFieldsUnset documents that a zero event
// (no membership store wired) leaves event_id/timestamp unset rather than
// emitting a placeholder "0" id.
func TestBuildJoinedEvent_ZeroEventLeavesFieldsUnset(t *testing.T) {
	got := buildJoinedEvent(eventsRoomRef(t), mustUserRef(t, "1", "Alice"), MembershipEvent{})

	if got.GetEventId() != "" {
		t.Errorf("EventId: got %q, want empty", got.GetEventId())
	}
	if got.GetTimestamp() != nil {
		t.Errorf("Timestamp: got %v, want nil", got.GetTimestamp())
	}
}

func TestBuildForwardMessage(t *testing.T) {
	ts := time.UnixMilli(12345)
	got := buildForwardMessage(eventsRoomRef(t), mustUserRef(t, "1", "Alice"), "hello", ts)

	if got.GetRoom().GetRoomId() != "4" || got.GetRoom().GetPublicCode() != "G000000004" {
		t.Errorf("Room: got %+v, want id=4 code=G000000004", got.GetRoom())
	}
	if got.GetSender().GetId() != "1" {
		t.Errorf("Sender.Id: got %q, want %q", got.GetSender().GetId(), "1")
	}
	if got.GetSender().GetName() != "Alice" {
		t.Errorf("Sender.Name: got %q, want %q", got.GetSender().GetName(), "Alice")
	}
	if got.GetText() != "hello" {
		t.Errorf("Text: got %q, want %q", got.GetText(), "hello")
	}
	if !got.GetTimestamp().AsTime().Equal(ts) {
		t.Errorf("Timestamp: got %v, want %v", got.GetTimestamp().AsTime(), ts)
	}
}

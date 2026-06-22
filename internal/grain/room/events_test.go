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

func TestBuildJoinedEvent(t *testing.T) {
	got := buildJoinedEvent(eventsRoomRef(t), mustUserRef(t, "1", "Alice"))

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
}

func TestBuildLeftEvent(t *testing.T) {
	got := buildLeftEvent(eventsRoomRef(t), mustUserRef(t, "1", "Alice"))

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

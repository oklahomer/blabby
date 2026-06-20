package room

import (
	"testing"
	"time"

	userpb "github.com/oklahomer/blabby/gen/user"
)

func TestBuildJoinedEvent(t *testing.T) {
	got := buildJoinedEvent("general", mustUserRef(t, "1", "Alice"))

	if got.GetRoomId() != "general" {
		t.Errorf("RoomId: got %q, want %q", got.GetRoomId(), "general")
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
	got := buildLeftEvent("general", mustUserRef(t, "1", "Alice"))

	if got.GetRoomId() != "general" {
		t.Errorf("RoomId: got %q, want %q", got.GetRoomId(), "general")
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
	got := buildForwardMessage("general", mustUserRef(t, "1", "Alice"), "hello", ts)

	if got.GetRoomId() != "general" {
		t.Errorf("RoomId: got %q, want %q", got.GetRoomId(), "general")
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

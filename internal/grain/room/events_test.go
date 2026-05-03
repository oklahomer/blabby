package room

import (
	"testing"
	"time"

	userpb "github.com/oklahomer/blabby/gen/user"
)

func TestBuildJoinedEvent(t *testing.T) {
	got := buildJoinedEvent("general", "alice")

	if got.GetRoomId() != "general" {
		t.Errorf("RoomId: got %q, want %q", got.GetRoomId(), "general")
	}
	if got.GetUserId() != "alice" {
		t.Errorf("UserId: got %q, want %q", got.GetUserId(), "alice")
	}
	if got.GetEventType() != userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED {
		t.Errorf("EventType: got %v, want JOINED", got.GetEventType())
	}
}

func TestBuildLeftEvent(t *testing.T) {
	got := buildLeftEvent("general", "alice")

	if got.GetRoomId() != "general" {
		t.Errorf("RoomId: got %q, want %q", got.GetRoomId(), "general")
	}
	if got.GetUserId() != "alice" {
		t.Errorf("UserId: got %q, want %q", got.GetUserId(), "alice")
	}
	if got.GetEventType() != userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT {
		t.Errorf("EventType: got %v, want LEFT", got.GetEventType())
	}
}

func TestBuildForwardMessage(t *testing.T) {
	ts := time.UnixMilli(12345)
	got := buildForwardMessage("general", "alice", "hello", ts)

	if got.GetRoomId() != "general" {
		t.Errorf("RoomId: got %q, want %q", got.GetRoomId(), "general")
	}
	if got.GetSenderId() != "alice" {
		t.Errorf("SenderId: got %q, want %q", got.GetSenderId(), "alice")
	}
	if got.GetText() != "hello" {
		t.Errorf("Text: got %q, want %q", got.GetText(), "hello")
	}
	if !got.GetTimestamp().AsTime().Equal(ts) {
		t.Errorf("Timestamp: got %v, want %v", got.GetTimestamp().AsTime(), ts)
	}
}

package id

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

// TestLogValuer_JSONHandler is the load-bearing test for the LogValuer
// implementations: it pipes a UserID and a RoomID through the same
// slog.JSONHandler that the production binary uses and asserts the
// rendered values are strings, not the empty objects encoding/json
// would produce for a struct with an unexported field.
func TestLogValuer_JSONHandler(t *testing.T) {
	uid, err := NewUserID("alice")
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	rid, err := NewRoomID("general")
	if err != nil {
		t.Fatalf("NewRoomID: %v", err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("test", "user_id", uid, "room_id", rid)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("unmarshal log line: %v\nraw: %s", err, buf.String())
	}

	if got, want := line["user_id"], "alice"; got != want {
		t.Errorf("user_id rendered as %v (%T), want %q", got, got, want)
	}
	if got, want := line["room_id"], "general"; got != want {
		t.Errorf("room_id rendered as %v (%T), want %q", got, got, want)
	}
}

// TestLogValue_ReturnsStringValue confirms the slog.Value kind, so a
// future handler change that special-cases KindString still picks up
// the identifier value.
func TestLogValue_ReturnsStringValue(t *testing.T) {
	uid, _ := NewUserID("alice")
	rid, _ := NewRoomID("general")

	if got := uid.LogValue().Kind(); got != slog.KindString {
		t.Errorf("UserID.LogValue().Kind() = %v, want slog.KindString", got)
	}
	if got := uid.LogValue().String(); got != "alice" {
		t.Errorf("UserID.LogValue().String() = %q, want %q", got, "alice")
	}
	if got := rid.LogValue().Kind(); got != slog.KindString {
		t.Errorf("RoomID.LogValue().Kind() = %v, want slog.KindString", got)
	}
	if got := rid.LogValue().String(); got != "general" {
		t.Errorf("RoomID.LogValue().String() = %q, want %q", got, "general")
	}
}

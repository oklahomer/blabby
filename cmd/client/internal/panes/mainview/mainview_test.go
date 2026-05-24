package mainview

import (
	"strings"
	"testing"
)

func TestViewDefaultLabel(t *testing.T) {
	out := View(State{}, false, 50, 20)
	if !strings.Contains(out, "(no room selected)") {
		t.Errorf("missing default room label:\n%s", out)
	}
	if !strings.Contains(out, "(no messages yet)") {
		t.Errorf("missing scrollback placeholder:\n%s", out)
	}
	if !strings.Contains(out, "(select a room to start typing)") {
		t.Errorf("missing input placeholder:\n%s", out)
	}
}

func TestViewRoomLabel(t *testing.T) {
	out := View(State{RoomLabel: "general"}, false, 50, 20)
	if !strings.Contains(out, "general") {
		t.Errorf("missing room label:\n%s", out)
	}
}

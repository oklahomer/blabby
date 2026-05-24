package rooms

import (
	"strings"
	"testing"
)

func TestViewShowsTitleAndPlaceholder(t *testing.T) {
	out := View(State{}, false, 20, 20)
	if !strings.Contains(out, "Rooms") {
		t.Errorf("missing Rooms title:\n%s", out)
	}
	if !strings.Contains(out, "(no rooms yet)") {
		t.Errorf("missing placeholder:\n%s", out)
	}
}

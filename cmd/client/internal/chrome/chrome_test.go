package chrome

import (
	"strings"
	"testing"
)

func TestRenderIncludesAllPaneContent(t *testing.T) {
	t.Parallel()
	out := Render(State{
		Width:     100,
		Height:    30,
		RoomsView: "ROOMS_MARKER",
		MainView:  "MAIN_MARKER",
		InfoView:  "INFO_MARKER",
	})
	for _, marker := range []string{"ROOMS_MARKER", "MAIN_MARKER", "INFO_MARKER"} {
		if !strings.Contains(out, marker) {
			t.Errorf("rendered chrome missing %q:\n%s", marker, out)
		}
	}
}

func TestRenderFallsBackToTooSmallNotice(t *testing.T) {
	t.Parallel()
	out := Render(State{Width: 40, Height: 10})
	if !strings.Contains(out, "Terminal too small") {
		t.Fatalf("expected too-small notice, got:\n%s", out)
	}
}

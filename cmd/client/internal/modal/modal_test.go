package modal

import (
	"strings"
	"testing"
)

func TestOverlayPreservesModalContent(t *testing.T) {
	t.Parallel()
	background := "(unused for sizing)"
	out := Overlay(background, "MODAL_BODY", 80, 24)
	if !strings.Contains(out, "MODAL_BODY") {
		t.Fatalf("modal body missing from overlay output:\n%s", out)
	}
}

func TestOverlayMatchesRequestedDimensions(t *testing.T) {
	t.Parallel()
	out := Overlay("", "x", 40, 10)
	lines := strings.Split(out, "\n")
	if len(lines) != 10 {
		t.Fatalf("expected 10 lines, got %d", len(lines))
	}
}

func TestOverlayCompositesModalOntoBackground(t *testing.T) {
	t.Parallel()
	// 5x5 background of dots so we can see what survives around the modal.
	var bgRows []string
	for i := 0; i < 5; i++ {
		bgRows = append(bgRows, "BGBGB")
	}
	bg := strings.Join(bgRows, "\n")

	// 3x1 modal centred at row 2, columns 1..4.
	out := Overlay(bg, "MMM", 5, 5)

	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %q", len(lines), lines)
	}

	// Row 2 should be "B" + "MMM" + "B" — background visible on both sides.
	if lines[2] != "BMMMB" {
		t.Fatalf("modal row = %q, want %q", lines[2], "BMMMB")
	}
	// Surrounding rows must remain untouched background.
	for _, i := range []int{0, 1, 3, 4} {
		if lines[i] != "BGBGB" {
			t.Errorf("row %d = %q, want untouched background BGBGB", i, lines[i])
		}
	}
}

func TestOverlayFallsBackWhenModalLargerThanScreen(t *testing.T) {
	t.Parallel()
	bg := "ignored"
	out := Overlay(bg, "HUGE_MODAL_CONTENT", 5, 3)
	// Should not panic; should render the modal somehow.
	if !strings.Contains(out, "HUGE_MODAL_CONTENT") {
		t.Fatalf("modal missing from fallback render:\n%s", out)
	}
}

func TestOverlayHandlesEmptyBackground(t *testing.T) {
	t.Parallel()
	out := Overlay("", "MODAL", 20, 5)
	if !strings.Contains(out, "MODAL") {
		t.Fatal("modal missing from empty-background render")
	}
}

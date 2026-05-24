package ui

import (
	"strings"
	"testing"
)

func TestPaneBorderColorChangesWithFocus(t *testing.T) {
	unfocused := PaneBorder(false).GetBorderTopForeground()
	focused := PaneBorder(true).GetBorderTopForeground()

	if unfocused == focused {
		t.Fatalf("expected focused border color to differ from unfocused; both = %v", focused)
	}
}

func TestStylesProduceNonEmptyRender(t *testing.T) {
	tests := []struct {
		name   string
		render string
	}{
		{"title", Title().Render("Rooms")},
		{"subtle", Subtle().Render("(no rooms yet)")},
		{"error", Error().Render("✗ Invalid credentials")},
		{"label", Label().Render("User:")},
		{"modal", ModalBorder().Render("Sign in")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if strings.TrimSpace(tc.render) == "" {
				t.Fatalf("expected non-empty render, got %q", tc.render)
			}
		})
	}
}

package chrome

import (
	"errors"
	"testing"
)

func TestComputeWidthsSumToTotal(t *testing.T) {
	tests := []struct {
		name  string
		w, h  int
		wantL int
		wantR int
	}{
		{"80x24 typical", 80, 24, 16, 20},
		{"100x30 wider", 100, 30, 20, 25},
		{"120x40 wider still", 120, 40, 24, 30},
		{"60x20 minimum", 60, 20, 12, 15},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, err := Compute(tc.w, tc.h)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if l.LeftW != tc.wantL {
				t.Errorf("LeftW = %d, want %d", l.LeftW, tc.wantL)
			}
			if l.RightW != tc.wantR {
				t.Errorf("RightW = %d, want %d", l.RightW, tc.wantR)
			}
			if got := l.LeftW + l.MiddleW + l.RightW; got != tc.w {
				t.Errorf("widths sum to %d, want %d", got, tc.w)
			}
			if l.MiddleW <= 0 {
				t.Errorf("middle pane collapsed: %d", l.MiddleW)
			}
		})
	}
}

func TestLayoutInnerDimensionsSubtractBorderBudget(t *testing.T) {
	l, err := Compute(120, 40)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Two rows of border (top + bottom) come off the height; two
	// columns (left + right) come off the middle pane's width.
	if got, want := l.InnerHeight(), l.Height-2; got != want {
		t.Errorf("InnerHeight() = %d, want %d", got, want)
	}
	if got, want := l.MiddleInnerWidth(), l.MiddleW-2; got != want {
		t.Errorf("MiddleInnerWidth() = %d, want %d", got, want)
	}
}

func TestComputeRejectsTooSmall(t *testing.T) {
	tests := []struct {
		name string
		w, h int
	}{
		{"too narrow", 59, 24},
		{"too short", 80, 19},
		{"both small", 40, 10},
		{"zero", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compute(tc.w, tc.h)
			if !errors.Is(err, ErrTerminalTooSmall) {
				t.Fatalf("expected ErrTerminalTooSmall, got %v", err)
			}
		})
	}
}

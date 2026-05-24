// Package chrome owns the persistent three-pane layout that frames
// every TUI screen. Layout computation is a pure function of terminal
// dimensions; rendering composes the per-pane View strings into the
// final frame. The chrome stays the same across login, idle, and
// chat-in-flight; modals are overlaid on top via the modal package.
package chrome

import (
	"errors"
	"math"
)

// ErrTerminalTooSmall is returned by Compute when the terminal is
// too small to render the three-pane layout meaningfully. Callers
// (Render in particular) fall back to a resize prompt.
var ErrTerminalTooSmall = errors.New("terminal too small")

// MinWidth and MinHeight are the smallest terminal dimensions the
// three-pane chrome will attempt to render. Below either threshold,
// Compute returns ErrTerminalTooSmall.
const (
	MinWidth  = 60
	MinHeight = 20

	leftFrac  = 0.20
	rightFrac = 0.25
)

// Layout holds the computed pane dimensions for the current terminal
// size. Widths sum to the full terminal width; the middle pane
// receives any rounding remainder so the layout never under- or
// over-fills.
type Layout struct {
	Width  int
	Height int

	LeftW   int
	MiddleW int
	RightW  int
}

// Compute returns the pane widths for the given terminal dimensions.
// width and height are the dimensions of the full alternate screen
// the chrome will paint into. The proportions target roughly
// 20% / 55% / 25%, biased so the middle pane absorbs any leftover
// columns rounded off by Floor.
func Compute(width, height int) (Layout, error) {
	if width < MinWidth || height < MinHeight {
		return Layout{}, ErrTerminalTooSmall
	}
	left := int(math.Floor(float64(width) * leftFrac))
	right := int(math.Floor(float64(width) * rightFrac))
	middle := width - left - right
	return Layout{
		Width:   width,
		Height:  height,
		LeftW:   left,
		MiddleW: middle,
		RightW:  right,
	}, nil
}

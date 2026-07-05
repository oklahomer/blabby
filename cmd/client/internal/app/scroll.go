package app

import (
	"github.com/oklahomer/blabby/cmd/client/internal/panes/mainview"
)

// clampOffset bounds a scroll offset into [0, maxOffset]. Offset 0 pins the
// view to the newest line at the bottom; maxOffset shows the oldest.
func clampOffset(offset, maxOffset int) int {
	if offset < 0 {
		return 0
	}
	if offset > maxOffset {
		return maxOffset
	}
	return offset
}

// maxScrollOffset is how far the view can scroll up: the number of rows
// that do not fit on screen, or 0 when everything fits.
func maxScrollOffset(totalLines, avail int) int {
	if over := totalLines - avail; over > 0 {
		return over
	}
	return 0
}

// pageStep is how many rows a pgup/pgdn jump moves — one screen less a row
// of overlap, and always at least one.
func pageStep(avail int) int {
	if avail > 1 {
		return avail - 1
	}
	return 1
}

// adjustOffset keeps a scrolled-up viewport anchored to the same content
// after a room's rendered lines changed from oldLines to newLines. When the
// bottom (below the anchor) is unchanged, the growth landed above the
// viewport — a prepended backfill page — and the offset holds. When the
// bottom changed, newer lines arrived below and the offset slides up by the
// growth so the anchored content stays put. A pinned view (offset 0) always
// stays pinned and follows the newest line.
func adjustOffset(oldLines, newLines []mainview.Line, offset int) int {
	if offset <= 0 {
		return 0
	}
	delta := len(newLines) - len(oldLines)
	if delta <= 0 {
		// A dedup (no growth) or a trim (net shrink): leave the offset for
		// the render-time clamp to bound.
		return offset
	}
	tail := offset
	if tail > len(oldLines) {
		tail = len(oldLines)
	}
	if trailingLinesEqual(oldLines, newLines, tail) {
		return offset
	}
	return offset + delta
}

// trailingLinesEqual reports whether the last n lines of oldLines and
// newLines refer to the same entries. Lines are compared by identity (event
// id, or separator date) rather than by value, avoiding a time.Time
// equality check.
func trailingLinesEqual(oldLines, newLines []mainview.Line, n int) bool {
	for i := 1; i <= n; i++ {
		oi, ni := len(oldLines)-i, len(newLines)-i
		if oi < 0 || ni < 0 {
			return false
		}
		if !lineIdentityEqual(oldLines[oi], newLines[ni]) {
			return false
		}
	}
	return true
}

// lineIdentityEqual compares two rendered lines by identity: separators by
// their date, message rows by their event id.
func lineIdentityEqual(a, b mainview.Line) bool {
	if a.Separator != b.Separator {
		return false
	}
	if a.Separator {
		return a.Date == b.Date
	}
	return a.Msg.ID == b.Msg.ID
}

// Package ui holds the lipgloss styling primitives shared across the
// TUI client packages. Concentrating the palette and border styles here
// keeps the chrome, panes, and modals visually consistent without
// scattering colour constants across the codebase.
package ui

import "github.com/charmbracelet/lipgloss"

// Palette holds the small set of named colours used by the TUI.
// Keeping the palette explicit (rather than peppering lipgloss.Color
// calls throughout) makes it cheap to retune later.
const (
	ColorBorder        = lipgloss.Color("240") // dim grey for unfocused borders
	ColorBorderFocused = lipgloss.Color("69")  // bright accent for the focused pane
	ColorTitle         = lipgloss.Color("252") // pane / modal title text
	ColorSubtle        = lipgloss.Color("245") // placeholder / hint text
	ColorError         = lipgloss.Color("203") // login error rows
	ColorAccent        = lipgloss.Color("212") // user / id labels
	ColorSuccess       = lipgloss.Color("78")  // success notices ("account verified")
)

// PaneBorder returns the border style for a pane. When focused, the
// border colour switches to ColorBorderFocused — this is the sole
// focus indicator the TUI uses; no glyph, no bolded title.
func PaneBorder(focused bool) lipgloss.Style {
	colour := ColorBorder
	if focused {
		colour = ColorBorderFocused
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(colour)
}

// ModalBorder returns the border style applied to modal overlays.
// Modals always render with the accent colour so they stand out from
// the chrome behind them.
func ModalBorder() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorderFocused).
		Padding(0, 1)
}

// Title returns the style used for pane and section headings.
func Title() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(ColorTitle)
}

// Subtle returns the style used for placeholder and hint text.
func Subtle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(ColorSubtle)
}

// Error returns the style used for inline error rows.
func Error() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(ColorError)
}

// Success returns the style used for inline success notices.
func Success() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(ColorSuccess)
}

// Label returns the style used for labelled rows.
func Label() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(ColorAccent)
}

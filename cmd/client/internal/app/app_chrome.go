package app

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// updateChrome handles the terminal-chrome message family: window sizing,
// the recurring clock tick, key dispatch, and quit. It reports handled=false
// for anything outside the family so Update can probe the next one.
func (m Model) updateChrome(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		// Clamp negative dimensions at the boundary so downstream
		// layout / lipgloss calls never see undefined sizes.
		if v.Width > 0 {
			m.width = v.Width
		} else {
			m.width = 0
		}
		if v.Height > 0 {
			m.height = v.Height
		} else {
			m.height = 0
		}
		m.composer.Width = composerWidth(m.width, m.height)
		return m, nil, true

	case tickMsg:
		m.now = time.Time(v)
		m.infoState.Now = m.now
		return m, tickEverySecond(), true

	case tea.KeyMsg:
		next, cmd := m.handleKey(v)
		return next, cmd, true

	case tea.QuitMsg:
		// Any quit path — ctrl+c, SIGTERM via tea.WithContext, esc
		// from the login modal — runs through here. Close the conn
		// with a normal-closure frame so the server sees a clean
		// disconnect; the read loop's deferred Close runs after its
		// ReadMessage returns the close error.
		if m.conn != nil {
			api.CloseGracefully(m.conn)
		}
		return m, nil, true
	}
	return m, nil, false
}

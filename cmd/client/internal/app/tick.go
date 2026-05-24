package app

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tickMsg is the message delivered every second to drive the live
// clock in the Info pane. The wrapped time.Time is the moment the
// tick fired (in local time, as scheduled by tea.Tick).
type tickMsg time.Time

// tickEverySecond returns a Cmd that fires a tickMsg after one
// second. After handling the message, Update re-issues
// tickEverySecond() so the tick is self-perpetuating until the
// program quits. tea.Tick under the hood schedules a timer and
// emits the message on the tea runtime goroutine; no separate
// goroutine to clean up.
func tickEverySecond() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

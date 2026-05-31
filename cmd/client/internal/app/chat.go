package app

import (
	"sort"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/chrome"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/mainview"
)

const (
	// messageBucketCap bounds the per-room scrollback. Once a bucket
	// exceeds it, the oldest messages are dropped from the front so the
	// newest are always retained. Phase 1 keeps only in-session
	// messages — there is no history backfill — so a few hundred lines
	// per room is generous.
	messageBucketCap = 200

	// minComposerWidth floors the composer width so the cursor and a
	// few characters always render even on a very narrow terminal.
	minComposerWidth = 10

	// composerCharLimit is a loose client-side guard on composer
	// length. The server's 4 KiB text cap is authoritative; this just
	// stops a runaway paste from growing the field without bound.
	composerCharLimit = 4096
)

// newComposer builds the Main-pane message input. The component mirrors
// the login/search modals' textinput usage so focus, blur, and
// enter-submit behave consistently across the client.
func newComposer(width int) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "type a message…"
	ti.CharLimit = composerCharLimit
	ti.Width = width
	ti.Prompt = "> "
	return ti
}

// appendChatMessage inserts a decoded inbound message into the active
// room's bucket, ordered by the server timestamp. The sender name is
// resolved here and the user's own messages are flagged Self; mainview
// owns how Self is styled and never sees the raw user ID logic.
func (m Model) appendChatMessage(cm api.ChatMessageReceived) Model {
	if m.messages == nil {
		m.messages = map[string][]mainview.Message{}
	}
	// Show the human-readable name, falling back to the raw ID only if the
	// server sent no name (older frames or a directory miss). The user's own
	// messages are flagged Self so mainview can mute them — the name is still
	// shown, just dimmed, so other members stand out.
	sender := cm.SenderName
	if sender == "" {
		sender = cm.SenderID
	}
	msg := mainview.Message{Sender: sender, Text: cm.Text, At: cm.At, Self: cm.SenderID == m.userID}
	m.messages[cm.RoomID] = insertOrdered(m.messages[cm.RoomID], msg, messageBucketCap)
	return m
}

// insertOrdered returns a new slice with msg inserted into bucket by
// ascending At, preserving arrival order among equal timestamps. When
// the result exceeds cap, the oldest entries are dropped from the
// front. The bucket is never mutated in place: a fresh backing array is
// allocated so a value-copied Model cannot alias another's scrollback.
func insertOrdered(bucket []mainview.Message, msg mainview.Message, cap int) []mainview.Message {
	// First index whose timestamp is strictly after msg.At; inserting
	// there keeps equal-timestamp messages in arrival order.
	idx := sort.Search(len(bucket), func(i int) bool {
		return bucket[i].At.After(msg.At)
	})
	next := make([]mainview.Message, 0, len(bucket)+1)
	next = append(next, bucket[:idx]...)
	next = append(next, msg)
	next = append(next, bucket[idx:]...)
	if len(next) > cap {
		next = next[len(next)-cap:]
	}
	return next
}

// mainInnerDims returns the Main pane's inner content width and height
// for the current terminal size, or (0, 0) when the terminal is too
// small to lay out (the chrome paints a resize prompt in that case and
// the Main pane string is unused).
func mainInnerDims(termWidth, termHeight int) (width, height int) {
	layout, err := chrome.Compute(termWidth, termHeight)
	if err != nil {
		return 0, 0
	}
	return layout.MiddleInnerWidth(), layout.InnerHeight()
}

// composerWidth derives the textinput width from the Main pane's inner
// width, leaving a little room for the cursor and floored so the field
// is always usable.
func composerWidth(termWidth, termHeight int) int {
	w, _ := mainInnerDims(termWidth, termHeight)
	w -= 2
	if w < minComposerWidth {
		w = minComposerWidth
	}
	return w
}

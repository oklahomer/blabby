package app

import (
	"sort"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/chrome"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/mainview"
)

const (
	// eventBucketCap bounds the per-room scrollback in memory. Once a
	// bucket exceeds it, the oldest entries are dropped from the front so
	// the newest are always retained; the trimmed region can be re-fetched
	// by scrolling up, since history backfill re-reads the server timeline.
	eventBucketCap = 1000

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

// appendChatMessage inserts a decoded inbound message into its room's
// bucket, ordered and deduped by event id. The sender name is resolved
// here and the user's own messages are flagged Self; mainview owns how
// Self is styled and never sees the raw user code logic.
func (m Model) appendChatMessage(cm api.ChatMessageReceived) Model {
	// Show the human-readable name, falling back to the raw code only if the
	// server sent no name (a directory miss). The user's own messages are
	// flagged Self so mainview can mute them — the name is still shown, just
	// dimmed, so other members stand out.
	sender := cm.Sender.Name
	if sender == "" {
		sender = cm.Sender.ID
	}
	return m.appendEvent(cm.RoomID, mainview.Message{
		ID:     cm.EventID,
		Kind:   mainview.KindChat,
		Sender: sender,
		Text:   cm.Text,
		At:     cm.At,
		Self:   cm.Sender.ID == m.userID,
	})
}

// appendMemberEvent inserts a decoded membership frame as a system line in
// its room's bucket, ordered and deduped by event id like a chat message.
func (m Model) appendMemberEvent(me api.MemberEventReceived) Model {
	name := me.User.Name
	if name == "" {
		name = me.User.ID
	}
	kind := mainview.KindJoined
	if me.Kind == api.MemberLeft {
		kind = mainview.KindLeft
	}
	return m.appendEvent(me.RoomID, mainview.Message{
		ID:     me.EventID,
		Kind:   kind,
		Sender: name,
		At:     me.At,
	})
}

// appendEvent is the single insertion point every live frame and every
// backfilled entry flows through: it inserts msg into roomID's bucket,
// ordered and deduped by event id.
func (m Model) appendEvent(roomID string, msg mainview.Message) Model {
	if m.messages == nil {
		m.messages = map[string][]mainview.Message{}
	}
	next, _, _ := insertOrdered(m.messages[roomID], msg, eventBucketCap)
	m.messages[roomID] = next
	return m
}

// insertOrdered returns a new slice with msg inserted into bucket by
// ascending event id, together with whether it was inserted (false when
// the id was already present — a duplicate, e.g. a live frame that also
// arrived via backfill) and how many oldest entries were trimmed to
// respect cap. The bucket is never mutated in place: on insertion a fresh
// backing array is allocated so a value-copied Model cannot alias
// another's scrollback; a duplicate returns the original slice unchanged.
func insertOrdered(bucket []mainview.Message, msg mainview.Message, cap int) (next []mainview.Message, inserted bool, trimmed int) {
	idx := sort.Search(len(bucket), func(i int) bool {
		return bucket[i].ID >= msg.ID
	})
	if idx < len(bucket) && bucket[idx].ID == msg.ID {
		return bucket, false, 0
	}
	next = make([]mainview.Message, 0, len(bucket)+1)
	next = append(next, bucket[:idx]...)
	next = append(next, msg)
	next = append(next, bucket[idx:]...)
	if len(next) > cap {
		trimmed = len(next) - cap
		next = next[trimmed:]
	}
	return next, true, trimmed
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

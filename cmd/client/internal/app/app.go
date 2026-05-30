package app

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/chrome"
	"github.com/oklahomer/blabby/cmd/client/internal/login"
	"github.com/oklahomer/blabby/cmd/client/internal/modal"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/info"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/mainview"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/rooms"
	"github.com/oklahomer/blabby/cmd/client/internal/roomsearch"
)

// Model is the root tea.Model for the TUI client. State transitions
// — login modal open/closed, focus moves, WebSocket connect /
// disconnect — happen only in Update; sub-Models report typed
// outcomes which Update maps to the next state.
type Model struct {
	server     *url.URL
	httpClient *http.Client
	program    api.FrameSender // captured at construction; used by the WS read loop
	ctx        context.Context // program lifecycle; cancelled on SIGTERM/SIGINT

	// chrome state
	width  int
	height int
	now    time.Time
	focus  focusTarget

	// pane state
	roomsState    rooms.State
	mainviewState mainview.State
	infoState     info.State

	// modal state — at most one modal at a time
	modal modal.Modal

	// session state populated after the WS handshake succeeds
	token    string
	username string
	userID   string
	conn     *websocket.Conn

	// active-room state populated after the user joins or selects a
	// room. activeRoomID is empty pre-join; nameForID maps a joined
	// room's ID to the friendly name captured at join time.
	activeRoomID string
	nameForID    map[string]string
}

// New constructs the root Model for the given parsed --server URL.
// The HTTP client is held without a timeout — per-request timeouts
// live inside the api.* Cmds. ctx defaults to context.Background()
// here; main overrides it via SetContext so signal cancellation can
// unwind the WebSocket read loop.
func New(server *url.URL, httpClient *http.Client) Model {
	now := time.Now()
	return Model{
		server:     server,
		httpClient: httpClient,
		ctx:        context.Background(),
		now:        now,
		focus:      focusRooms,
		infoState:  info.State{Server: server.String(), Now: now},
	}
}

// SetProgram lets main install the *tea.Program pointer after
// construction. The pointer is needed by ReadLoopCmd to call Send
// from the background goroutine. Splitting this out of New keeps
// the constructor signature stable for tests that construct the
// Model directly without spinning up a tea.Program.
func (m Model) SetProgram(p api.FrameSender) Model {
	m.program = p
	return m
}

// SetContext installs the program lifecycle context. ReadLoopCmd
// uses this context to unblock its goroutine on SIGTERM / SIGINT
// so the read loop does not outlive the bubbletea program.
func (m Model) SetContext(ctx context.Context) Model {
	m.ctx = ctx
	return m
}

// Init returns the initial Cmds — textinput blink + the recurring
// clock tick — that Bubble Tea runs before the first frame. The
// login modal is installed at construction time by main via
// OpenLoginModal(), not as a tea.Cmd from here.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		tickEverySecond(),
	)
}

// loginSubmitter returns the function the login modal calls when
// the user presses enter with both fields populated. It wires the
// HTTP login Cmd into the modal without leaking server-URL state
// across packages.
func (m Model) loginSubmitter() login.Submitter {
	return func(username, password string) tea.Cmd {
		return api.LoginCmd(m.httpClient, m.server.String(), username, password, api.DefaultLoginTimeout)
	}
}

// joinRoomSubmitter returns the closure the search modal calls when
// the user presses enter on a row. The closure captures the current
// session's HTTP client + token so the modal does not need to know
// about them.
func (m Model) joinRoomSubmitter() roomsearch.Submitter {
	return func(roomID, roomName string) tea.Cmd {
		return api.JoinRoomCmd(m.httpClient, m.server.String(), m.token, roomID, roomName, api.DefaultRoomCallTimeout)
	}
}

// roomsLoader returns the closure the search modal invokes from Init
// to fetch the server catalogue. Same seam pattern as loginSubmitter.
func (m Model) roomsLoader() roomsearch.Loader {
	return func() tea.Cmd {
		return api.LoadRoomsCmd(m.httpClient, m.server.String(), m.token, api.DefaultRoomCallTimeout)
	}
}

// Update routes incoming messages through the state-machine
// dispatch table. Every behavior transition lives here — sub-Models
// surface outcomes via typed messages and Update maps each outcome
// to the next state.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		return m, nil

	case tickMsg:
		m.now = time.Time(v)
		m.infoState.Now = m.now
		return m, tickEverySecond()

	case tea.KeyMsg:
		return m.handleKey(v)

	case api.LoginSucceeded:
		m.token = v.Token
		m.username = v.Username
		nextModal, _ := m.advanceLoginToConnecting()
		m.modal = nextModal
		return m, api.DialAndAuthCmd(m.server.String(), m.token, api.DefaultWSDialTimeout, api.DefaultWSAuthTimeout)

	case api.WSAuthSucceeded:
		// SetProgram is a construction-time contract enforced by
		// main. A nil program here means the read loop would never
		// run — fail loudly instead of silently authenticating.
		if m.program == nil {
			panic("app.Model: program not set; main must call SetProgram before Run")
		}
		m.conn = v.Conn
		m.userID = v.UserID
		m.infoState.Username = m.username
		m.infoState.UserID = m.userID
		m.modal = nil
		m.focus = focusRooms
		m.roomsState.Loading = true
		return m, tea.Batch(
			api.ReadLoopCmd(m.ctx, m.program, m.conn),
			api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, api.DefaultRoomCallTimeout),
		)

	case api.LoginRejected, api.LoginTransportError, api.WSAuthRejected, api.WSDialFailed, api.WSAuthTimedOut:
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd
		}
		return m, nil

	case api.JoinedRoomsLoaded:
		m.roomsState.JoinedIDs = v.RoomIDs
		m.roomsState.Loading = false
		m.roomsState.LoadError = ""
		if len(v.RoomIDs) == 0 {
			m.roomsState.Cursor = 0
		} else if m.roomsState.Cursor > len(v.RoomIDs)-1 {
			m.roomsState.Cursor = len(v.RoomIDs) - 1
		}
		return m, nil

	case api.JoinedRoomsLoadFailed:
		if v.HTTPStatus == http.StatusUnauthorized {
			return m.handleSessionExpiry()
		}
		m.roomsState.Loading = false
		m.roomsState.LoadError = api.Humanise(v.Status, v.Message)
		return m, nil

	case api.RoomJoined:
		if m.token == "" || m.conn == nil {
			// Join HTTP completed after the session ended
			// (WSDisconnected raced the response). Drop the message —
			// the user is already looking at a fresh login modal.
			return m, nil
		}
		if m.nameForID == nil {
			m.nameForID = map[string]string{}
		}
		m.nameForID[v.RoomID] = v.RoomName
		m.roomsState.NameForID = m.nameForID
		m.activeRoomID = v.RoomID
		m.mainviewState.RoomLabel = v.RoomName
		m.modal = nil
		// Reload from the server so the Rooms pane reflects the
		// authoritative membership; the modal's optimistic-add does
		// not own this state.
		return m, api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, api.DefaultRoomCallTimeout)

	case api.RoomsLoaded:
		if m.modal != nil {
			next, cmd := m.modal.Update(msg)
			m.modal = next
			return m, cmd
		}
		return m, nil

	case api.RoomsLoadFailed:
		if v.HTTPStatus == http.StatusUnauthorized {
			return m.handleSessionExpiry()
		}
		if m.modal != nil {
			next, cmd := m.modal.Update(msg)
			m.modal = next
			return m, cmd
		}
		return m, nil

	case api.RoomJoinFailed:
		if v.HTTPStatus == http.StatusUnauthorized {
			return m.handleSessionExpiry()
		}
		if m.modal != nil {
			next, cmd := m.modal.Update(msg)
			m.modal = next
			return m, cmd
		}
		return m, nil

	case api.WSFrameReceived:
		// joined / left / message / error / ping frames are dropped
		// silently here; the room-list and chat panes will register
		// handlers when they grow real state.
		return m, nil

	case api.WSDisconnected:
		previousUsername := m.username
		m.conn = nil
		m.token = ""
		m.username = ""
		m.userID = ""
		m.infoState.Username = ""
		m.infoState.UserID = ""
		m.activeRoomID = ""
		m.nameForID = nil
		m.mainviewState.RoomLabel = ""
		m.roomsState = rooms.State{}
		m.modal = m.openLoginModalWithError("Connection lost — please sign in again", previousUsername)
		return m, m.modal.Init()

	case tea.QuitMsg:
		// Any quit path — ctrl+c, SIGTERM via tea.WithContext, esc
		// from the login modal — runs through here. Close the conn
		// with a normal-closure frame so the server sees a clean
		// disconnect; the read loop's deferred Close runs after its
		// ReadMessage returns the close error.
		if m.conn != nil {
			api.CloseGracefully(m.conn)
		}
		return m, nil
	}

	if m.modal != nil {
		next, cmd := m.modal.Update(msg)
		m.modal = next
		return m, cmd
	}
	return m, nil
}

// handleKey applies the chrome's modal/focus dispatch rules: ctrl+c
// quits unconditionally; a modal absorbs everything else when open;
// `/` opens the search modal post-auth; otherwise focus keys are
// interpreted before passing the event to the focused pane.
func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		// The actual conn shutdown happens in the tea.QuitMsg
		// branch so every quit path (signal, esc from login, ctrl+c)
		// flows through the same close path.
		return m, tea.Quit
	}

	if m.modal != nil {
		next, cmd := m.modal.Update(k)
		m.modal = next
		return m, cmd
	}

	if k.String() == "/" {
		m.modal = roomsearch.New(m.joinRoomSubmitter(), m.roomsLoader(), m.server.String())
		return m, m.modal.Init()
	}

	if nextFocus, consumed := interpret(k.String(), m.focus); consumed {
		m.focus = nextFocus
		return m, nil
	}

	if m.focus == focusRooms {
		nextState, outcome := rooms.HandleKey(m.roomsState, k.String())
		m.roomsState = nextState
		switch outcome {
		case rooms.OutcomeSwitchActiveRoom:
			if id := nextState.ActiveID(); id != "" {
				m.activeRoomID = id
				m.mainviewState.RoomLabel = nextState.ResolveName(id)
			}
			return m, nil
		case rooms.OutcomeRetryLoad:
			m.roomsState.Loading = true
			m.roomsState.LoadError = ""
			return m, api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, api.DefaultRoomCallTimeout)
		}
		return m, nil
	}

	// focusMainView and focusMainInput are placeholders until
	// Story 4.3 wires send/receive into the Main pane.
	return m, nil
}

// handleSessionExpiry discards the current JWT, closes the WebSocket
// with a normal-closure frame, and reopens the login modal with the
// "Session expired" headline and the prior username pre-filled. Mirrors
// the WSDisconnected recovery path.
func (m Model) handleSessionExpiry() (tea.Model, tea.Cmd) {
	previousUsername := m.username
	api.CloseGracefully(m.conn)
	m.conn = nil
	m.token = ""
	m.username = ""
	m.userID = ""
	m.infoState.Username = ""
	m.infoState.UserID = ""
	m.activeRoomID = ""
	m.nameForID = nil
	m.mainviewState.RoomLabel = ""
	m.roomsState = rooms.State{}
	m.modal = m.openLoginModalWithError("Session expired — please sign in again", previousUsername)
	return m, m.modal.Init()
}

// View renders the chrome with the current pane content and, if a
// modal is open, overlays it on top.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	background := chrome.Render(chrome.State{
		Width:        m.width,
		Height:       m.height,
		RoomsView:    rooms.View(m.roomsState, m.focus == focusRooms, 0, 0),
		MainView:     mainview.View(m.mainviewState, m.focus == focusMainView, 0, 0),
		InfoView:     info.View(m.infoState, false, 0, 0),
		FocusedRooms: m.focus == focusRooms,
		FocusedMain:  m.focus == focusMainView || m.focus == focusMainInput,
	})

	if m.modal == nil {
		return background
	}
	return modal.Overlay(background, m.modal.View(m.width, m.height), m.width, m.height)
}

// OpenLoginModal installs a fresh login modal as the active modal
// and returns the updated Model. main calls this after constructing
// the Model so the very first frame paints with the modal open.
func (m Model) OpenLoginModal() Model {
	m.modal = login.New(m.loginSubmitter(), m.server.String())
	return m
}

// advanceLoginToConnecting flips the open login modal's in-flight
// copy from "Signing in…" to "Connecting…" once the HTTP login has
// succeeded. Returns the new modal plus any cmd Update should
// dispatch alongside the transition (currently nil — the
// DialAndAuthCmd is dispatched by the caller).
func (m Model) advanceLoginToConnecting() (modal.Modal, tea.Cmd) {
	if lm, ok := m.modal.(login.Model); ok {
		return lm.SetConnecting(), nil
	}
	return m.modal, nil
}

// openLoginModalWithError opens a fresh login modal with the given
// headline pre-populated and the username field pre-filled from the
// last-known session. Used when the WS connection drops and the
// user has to re-authenticate from scratch.
func (m Model) openLoginModalWithError(headline string, prefillUsername string) modal.Modal {
	base := login.New(m.loginSubmitter(), m.server.String())
	if prefillUsername != "" {
		base = base.PrefillUsername(prefillUsername)
	}
	return base.ShowError(headline, "")
}

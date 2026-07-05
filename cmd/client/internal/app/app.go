package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/chrome"
	"github.com/oklahomer/blabby/cmd/client/internal/createroom"
	"github.com/oklahomer/blabby/cmd/client/internal/login"
	"github.com/oklahomer/blabby/cmd/client/internal/modal"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/info"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/mainview"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/rooms"
	"github.com/oklahomer/blabby/cmd/client/internal/register"
	"github.com/oklahomer/blabby/cmd/client/internal/roomsearch"
	"github.com/oklahomer/blabby/cmd/client/internal/verify"
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
	token             string
	email             string
	userID            string
	conn              *websocket.Conn
	sessionGeneration api.SessionGeneration

	// active-room state populated after the user joins or selects a
	// room. activeRoomID is empty pre-join; nameForID maps a joined
	// room's ID to the friendly name captured at join time.
	activeRoomID string
	nameForID    map[string]string

	// chat state populated after the WS handshake succeeds. composer is
	// the Main-pane message input; messages holds the per-room
	// scrollback buckets keyed by room ID; connected drives the passive
	// status indicator; mainError is the inline Main-pane error string.
	composer  textinput.Model
	messages  map[string][]mainview.Message
	connected bool
	mainError string
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
	return func(email, password string) tea.Cmd {
		return api.LoginCmd(m.httpClient, m.server.String(), email, password, api.DefaultLoginTimeout)
	}
}

// registerSubmitter returns the function the register modal calls when the
// user submits a valid account form. Same seam pattern as loginSubmitter.
func (m Model) registerSubmitter() register.Submitter {
	return func(email, handle, password string) tea.Cmd {
		return api.RegisterCmd(m.httpClient, m.server.String(), email, handle, password, api.DefaultRegistrationTimeout)
	}
}

// verifySubmitter returns the function the verify modal calls when the user
// submits a well-formed PIN.
func (m Model) verifySubmitter() verify.Submitter {
	return func(email, pin string) tea.Cmd {
		return api.VerifyEmailCmd(m.httpClient, m.server.String(), email, pin, api.DefaultRegistrationTimeout)
	}
}

// verifyResender returns the function the verify modal calls on ctrl+r to
// request a fresh PIN.
func (m Model) verifyResender() verify.Resender {
	return func(email string) tea.Cmd {
		return api.ResendVerificationCmd(m.httpClient, m.server.String(), email, api.DefaultRegistrationTimeout)
	}
}

// openVerifyModal builds the verify modal for the given registered email.
func (m Model) openVerifyModal(email string) modal.Modal {
	return verify.New(m.verifySubmitter(), m.verifyResender(), email, m.server.String())
}

// reopenLoginModal returns a fresh login modal, carrying prefillEmail into the
// email field when non-empty. Used by the register/verify cancel paths so the
// address the user already typed survives the way back — matching every other
// login-reopen path.
func (m Model) reopenLoginModal(prefillEmail string) modal.Modal {
	base := login.New(m.loginSubmitter(), m.server.String())
	if prefillEmail != "" {
		base = base.PrefillEmail(prefillEmail)
	}
	return base
}

// joinRoomSubmitter returns the closure the search modal calls when
// the user presses enter on a row. The closure captures the current
// session's HTTP client + token so the modal does not need to know
// about them.
func (m Model) joinRoomSubmitter() roomsearch.Submitter {
	return func(roomID, roomName string) tea.Cmd {
		return api.JoinRoomCmd(m.httpClient, m.server.String(), m.token, roomID, roomName, m.sessionGeneration, api.DefaultRoomCallTimeout)
	}
}

// roomsLoader returns the closure the search modal invokes to fetch one
// catalogue page (initial load, debounced q search, keyset paging). Same
// seam pattern as loginSubmitter.
func (m Model) roomsLoader() roomsearch.Loader {
	return func(query api.RoomQuery) tea.Cmd {
		return api.LoadRoomsCmd(m.httpClient, m.server.String(), m.token, query, m.sessionGeneration, api.DefaultRoomCallTimeout)
	}
}

// createRoomSubmitter returns the closure the create-room modal calls when
// the user submits a valid name.
func (m Model) createRoomSubmitter() createroom.Submitter {
	return func(name string) tea.Cmd {
		return api.CreateRoomCmd(m.httpClient, m.server.String(), m.token, name, m.sessionGeneration, api.DefaultRoomCallTimeout)
	}
}

// openRoomSearchModal builds a fresh search modal wired to this session.
func (m Model) openRoomSearchModal() modal.Modal {
	return roomsearch.New(m.joinRoomSubmitter(), m.roomsLoader(), m.server.String())
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
		m.composer.Width = composerWidth(m.width, m.height)
		return m, nil

	case tickMsg:
		m.now = time.Time(v)
		m.infoState.Now = m.now
		return m, tickEverySecond()

	case tea.KeyMsg:
		return m.handleKey(v)

	case api.LoginSucceeded:
		m.token = v.Token
		m.email = v.Email
		m.sessionGeneration++
		nextModal, _ := m.advanceLoginToConnecting()
		m.modal = nextModal
		return m, api.DialAndAuthCmd(api.DialAndAuthRequest{
			Server:      m.server.String(),
			Token:       m.token,
			Generation:  m.sessionGeneration,
			DialTimeout: api.DefaultWSDialTimeout,
			AuthTimeout: api.DefaultWSAuthTimeout,
		})

	case api.WSAuthSucceeded:
		if v.Generation != m.sessionGeneration {
			if v.Conn != nil {
				_ = v.Conn.Close()
			}
			return m, nil
		}
		// Auth completions only ever arrive while the login modal is showing
		// its Connecting… phase. Anything else — no modal (a same-generation
		// duplicate after the session is already up) or a different modal (the
		// user has moved on, e.g. into registration after a disconnect) — is
		// stale: close the handed-off conn and change nothing, rather than
		// silently discarding whatever modal is open.
		if _, ok := m.modal.(login.Model); !ok {
			if v.Conn != nil {
				_ = v.Conn.Close()
			}
			return m, nil
		}
		// SetProgram is a construction-time contract enforced by
		// main. A nil program here means the read loop would never
		// run — fail loudly instead of silently authenticating.
		if m.program == nil {
			panic("app.Model: program not set; main must call SetProgram before Run")
		}
		m.conn = v.Conn
		m.userID = v.UserID
		m.infoState.Email = m.email
		m.infoState.UserID = m.userID
		m.modal = nil
		m.focus = focusRooms
		m.roomsState.Loading = true
		m.connected = true
		m.messages = map[string][]mainview.Message{}
		m.composer = newComposer(composerWidth(m.width, m.height))
		return m, tea.Batch(
			api.ReadLoopCmd(api.ReadLoopRequest{
				Context:    m.ctx,
				Sender:     m.program,
				Conn:       m.conn,
				Generation: m.sessionGeneration,
			}),
			api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, m.sessionGeneration, api.DefaultRoomCallTimeout),
		)

	case api.LoginRejected:
		// A correct password against an unverified account routes to the
		// verify modal (with the attempted email) instead of rendering as a
		// login error; every other rejection is the modal's to display.
		if v.Status == api.StatusAccountPending {
			m.modal = m.openVerifyModal(v.Email)
			return m, m.modal.Init()
		}
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd
		}
		return m, nil

	case api.LoginTransportError, api.LoginProtocolError:
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd
		}
		return m, nil

	case login.CreateAccountRequested:
		m.modal = register.New(m.registerSubmitter(), m.server.String())
		return m, m.modal.Init()

	case register.Cancelled:
		m.modal = m.reopenLoginModal(v.Email)
		return m, m.modal.Init()

	case verify.Cancelled:
		m.modal = m.reopenLoginModal(v.Email)
		return m, m.modal.Init()

	case api.RegisterSucceeded:
		// A pending account exists (fresh or re-registered) and a PIN is on
		// its way; the verify modal takes over.
		m.modal = m.openVerifyModal(v.Email)
		return m, m.modal.Init()

	case api.VerifySucceeded:
		// The account is active; back to sign-in with the email prefilled so
		// only the password is left to type.
		m.modal = login.New(m.loginSubmitter(), m.server.String()).
			PrefillEmail(v.Email).
			ShowNotice("Account verified — sign in")
		return m, m.modal.Init()

	case api.WSAuthRejected:
		if v.Generation != m.sessionGeneration {
			return m, nil
		}
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd
		}
		return m, nil

	case api.WSDialFailed:
		if v.Generation != m.sessionGeneration {
			return m, nil
		}
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd
		}
		return m, nil

	case api.WSAuthTimedOut:
		if v.Generation != m.sessionGeneration {
			return m, nil
		}
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd
		}
		return m, nil

	case api.JoinedRoomsLoaded:
		if !m.liveRoomResult(v.Generation) {
			return m, nil
		}
		// Descriptors carry the name alongside the id, so the Rooms pane shows
		// real names after a reload without relying on an in-session capture.
		if m.nameForID == nil {
			m.nameForID = map[string]string{}
		}
		ids := make([]string, len(v.Rooms))
		for i, room := range v.Rooms {
			ids[i] = room.ID
			m.nameForID[room.ID] = room.Name
		}
		m.roomsState.JoinedIDs = ids
		m.roomsState.NameForID = m.nameForID
		m.roomsState.Loading = false
		m.roomsState.LoadError = ""
		// A reload replaces the list a half-armed leave gesture referred to;
		// disarm it so the confirm banner cannot name a stale room.
		m.roomsState.PendingLeaveID = ""
		if len(ids) == 0 {
			m.roomsState.Cursor = 0
		} else if m.roomsState.Cursor > len(ids)-1 {
			m.roomsState.Cursor = len(ids) - 1
		}
		return m, nil

	case api.JoinedRoomsLoadFailed:
		if !m.liveRoomResult(v.Generation) {
			return m, nil
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			return m.handleSessionExpiry()
		}
		m.roomsState.Loading = false
		m.roomsState.LoadError = api.Humanise(v.Status, v.Message)
		return m, nil

	case api.RoomJoined:
		if !m.liveRoomResult(v.Generation) {
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
		return m, api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, m.sessionGeneration, api.DefaultRoomCallTimeout)

	case roomsearch.CreateRoomRequested:
		if !m.hasLiveSession() {
			return m, nil
		}
		if _, ok := m.modal.(roomsearch.Model); !ok {
			return m, nil
		}
		m.modal = createroom.New(m.createRoomSubmitter(), m.server.String())
		return m, m.modal.Init()

	case createroom.Cancelled:
		if !m.hasLiveSession() {
			return m, nil
		}
		if _, ok := m.modal.(createroom.Model); !ok {
			return m, nil
		}
		m.modal = m.openRoomSearchModal()
		return m, m.modal.Init()

	case api.RoomCreated:
		if !m.liveRoomResult(v.Generation) {
			return m, nil
		}
		// The server seeded the caller as the room's owner, so this is a
		// completed join: activate the room and reload the authoritative list.
		if m.nameForID == nil {
			m.nameForID = map[string]string{}
		}
		m.nameForID[v.Room.ID] = v.Room.Name
		m.roomsState.NameForID = m.nameForID
		m.activeRoomID = v.Room.ID
		m.mainviewState.RoomLabel = v.Room.Name
		m.modal = nil
		return m, api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, m.sessionGeneration, api.DefaultRoomCallTimeout)

	case api.RoomCreateFailed:
		if !m.liveRoomResult(v.Generation) {
			return m, nil
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			return m.handleSessionExpiry()
		}
		if m.modal != nil {
			next, cmd := m.modal.Update(msg)
			m.modal = next
			return m, cmd
		}
		return m, nil

	case api.RoomLeft:
		if !m.liveRoomResult(v.Generation) {
			return m, nil
		}
		if m.activeRoomID == v.RoomID {
			// The active room is gone from the user's set; the Main pane
			// returns to its no-room placeholder.
			m.activeRoomID = ""
			m.mainviewState.RoomLabel = ""
			m.mainError = ""
		}
		m.roomsState.ActionError = ""
		return m, api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, m.sessionGeneration, api.DefaultRoomCallTimeout)

	case api.RoomLeaveFailed:
		if !m.liveRoomResult(v.Generation) {
			return m, nil
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			return m.handleSessionExpiry()
		}
		m.roomsState.ActionError = api.Humanise(v.Status, v.Message)
		return m, nil

	case api.RoomsLoaded:
		if !m.liveRoomResult(v.Generation) {
			return m, nil
		}
		if m.modal != nil {
			next, cmd := m.modal.Update(msg)
			m.modal = next
			return m, cmd
		}
		return m, nil

	case api.RoomsLoadFailed:
		if !m.liveRoomResult(v.Generation) {
			return m, nil
		}
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
		if !m.liveRoomResult(v.Generation) {
			return m, nil
		}
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
		if v.Generation != m.sessionGeneration {
			return m, nil
		}
		// message and error frames render into the Main pane; joined /
		// left / ping are delivered but ignored in this phase (see the
		// deferred-work notes for system lines and pong replies).
		switch v.Type {
		case "message":
			if cm, ok := api.DecodeChatMessage(v.Raw); ok {
				m = m.appendChatMessage(cm)
			}
			return m, nil
		case "error":
			if ef, ok := api.DecodeErrorFrame(v.Raw); ok {
				m.mainError = api.Humanise(ef.Status, ef.Message)
			}
			return m, nil
		default:
			return m, nil
		}

	case api.SendMessageSucceeded:
		if m.token == "" || m.conn == nil || v.Generation != m.sessionGeneration {
			return m, nil
		}
		// The sent message renders when its echo frame arrives over the
		// WebSocket, not here; the HTTP 200 only clears any prior error.
		m.mainError = ""
		return m, nil

	case api.SendMessageFailed:
		if m.token == "" || m.conn == nil || v.Generation != m.sessionGeneration {
			return m, nil
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			return m.handleSessionExpiry()
		}
		m.mainError = api.Humanise(v.Status, v.Message)
		return m, nil

	case api.WSDisconnected:
		if v.Generation != m.sessionGeneration {
			return m, nil
		}
		previousEmail := m.email
		m.conn = nil
		m.token = ""
		m.email = ""
		m.userID = ""
		m.infoState.Email = ""
		m.infoState.UserID = ""
		m.activeRoomID = ""
		m.nameForID = nil
		m.mainviewState.RoomLabel = ""
		m.roomsState = rooms.State{}
		m.connected = false
		m.messages = nil
		m.composer = textinput.Model{}
		m.mainError = ""
		m.modal = m.openLoginModalWithError("Connection lost — please sign in again", previousEmail)
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

	// `/` opens the room search globally, except while the composer is
	// focused — there it is a literal character of the message.
	if k.String() == "/" && m.focus != focusMainInput {
		m.modal = m.openRoomSearchModal()
		return m, m.modal.Init()
	}

	if nextFocus, consumed := interpret(k.String(), m.focus); consumed {
		leavingInput := m.focus == focusMainInput
		m.focus = nextFocus
		if leavingInput {
			m.composer.Blur()
		}
		// Entering the input region with an active room focuses the
		// composer; dispatch the blink Cmd it returns so the cursor
		// blinks. Without an active room the composer stays disabled.
		if nextFocus == focusMainInput && m.activeRoomID != "" {
			return m, m.composer.Focus()
		}
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
				// Drop any inline error left over from the previous
				// room so it does not linger over a different scrollback.
				m.mainError = ""
			}
			return m, nil
		case rooms.OutcomeRetryLoad:
			m.roomsState.Loading = true
			m.roomsState.LoadError = ""
			return m, api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, m.sessionGeneration, api.DefaultRoomCallTimeout)
		case rooms.OutcomeLeaveRoom:
			id := nextState.ActiveID()
			if id == "" {
				return m, nil
			}
			return m, api.LeaveRoomCmd(m.httpClient, m.server.String(), m.token,
				id, nextState.ResolveName(id), m.sessionGeneration, api.DefaultRoomCallTimeout)
		}
		return m, nil
	}

	if m.focus == focusMainInput {
		// enter sends the composed message over HTTP when there is a
		// non-empty value and an active room; the input clears on send,
		// and the message renders when its echo frame arrives. Enter
		// with empty text or no active room is a no-op.
		if k.String() == "enter" {
			text := strings.TrimSpace(m.composer.Value())
			if text == "" || m.activeRoomID == "" {
				return m, nil
			}
			cmd := api.SendMessageCmd(api.SendMessageCommandRequest{
				Client:     m.httpClient,
				Server:     m.server.String(),
				Token:      m.token,
				Generation: m.sessionGeneration,
				RoomID:     m.activeRoomID,
				Text:       text,
				Timeout:    api.DefaultRoomCallTimeout,
			})
			m.composer.SetValue("")
			return m, cmd
		}
		var cmd tea.Cmd
		m.composer, cmd = m.composer.Update(k)
		return m, cmd
	}

	// focusMainView has no key bindings in this phase; scrollback scroll
	// keys are out of scope (see the deferred-work notes).
	return m, nil
}

// hasLiveSession reports whether this model still owns an authenticated
// WebSocket session. Room HTTP completions are tied to the session that
// dispatched them; after disconnect, same-generation completions are stale too.
func (m Model) hasLiveSession() bool {
	return m.token != "" && m.conn != nil
}

// liveRoomResult reports whether a room HTTP result belongs to the current
// live session. Call this before handling unauthorized responses so stale 401s
// from an old request cannot expire a newer login.
func (m Model) liveRoomResult(generation api.SessionGeneration) bool {
	return m.hasLiveSession() && generation == m.sessionGeneration
}

// handleSessionExpiry discards the current JWT, closes the WebSocket
// with a normal-closure frame, and reopens the login modal with the
// "Session expired" headline and the prior email pre-filled. Mirrors
// the WSDisconnected recovery path.
func (m Model) handleSessionExpiry() (tea.Model, tea.Cmd) {
	previousEmail := m.email
	api.CloseGracefully(m.conn)
	m.conn = nil
	m.token = ""
	m.email = ""
	m.userID = ""
	m.infoState.Email = ""
	m.infoState.UserID = ""
	m.activeRoomID = ""
	m.nameForID = nil
	m.mainviewState.RoomLabel = ""
	m.roomsState = rooms.State{}
	m.connected = false
	m.messages = nil
	m.composer = textinput.Model{}
	m.mainError = ""
	m.modal = m.openLoginModalWithError("Session expired — please sign in again", previousEmail)
	return m, m.modal.Init()
}

// View renders the chrome with the current pane content and, if a
// modal is open, overlays it on top.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	mainW, mainH := mainInnerDims(m.width, m.height)
	mainState := mainview.State{
		RoomLabel: m.mainviewState.RoomLabel,
		Messages:  m.messages[m.activeRoomID],
		Composer:  m.composer.View(),
		ErrorLine: m.mainError,
		CanType:   m.activeRoomID != "",
		Connected: m.connected,
	}

	background := chrome.Render(chrome.State{
		Width:        m.width,
		Height:       m.height,
		RoomsView:    rooms.View(m.roomsState, m.focus == focusRooms, 0, 0),
		MainView:     mainview.View(mainState, m.focus == focusMainView, mainW, mainH),
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
// headline pre-populated and the email field pre-filled from the
// last-known session. Used when the WS connection drops and the
// user has to re-authenticate from scratch.
func (m Model) openLoginModalWithError(headline, prefillEmail string) modal.Modal {
	base := login.New(m.loginSubmitter(), m.server.String())
	if prefillEmail != "" {
		base = base.PrefillEmail(prefillEmail)
	}
	return base.ShowError(headline, "")
}

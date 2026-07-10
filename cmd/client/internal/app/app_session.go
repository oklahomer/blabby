package app

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/login"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/mainview"
	"github.com/oklahomer/blabby/cmd/client/internal/register"
	"github.com/oklahomer/blabby/cmd/client/internal/verify"
)

// updateSession handles the session/auth message family: HTTP login,
// registration and email verification, the WebSocket dial/auth handshake,
// and disconnect recovery. It reports handled=false for anything outside
// the family so Update can probe the next one.
func (m Model) updateSession(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch v := msg.(type) {
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
		}), true

	case api.WSAuthSucceeded:
		if v.Generation != m.sessionGeneration {
			if v.Conn != nil {
				_ = v.Conn.Close()
			}
			return m, nil, true
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
			return m, nil, true
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
		), true

	case api.LoginRejected:
		// A correct password against an unverified account routes to the
		// verify modal (with the attempted email) instead of rendering as a
		// login error; every other rejection is the modal's to display.
		if v.Status == api.StatusAccountPending {
			m.modal = m.openVerifyModal(v.Email)
			return m, m.modal.Init(), true
		}
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd, true
		}
		return m, nil, true

	case api.LoginTransportError, api.LoginProtocolError:
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd, true
		}
		return m, nil, true

	case login.CreateAccountRequested:
		m.modal = register.New(m.registerSubmitter(), m.server.String())
		return m, m.modal.Init(), true

	case register.Cancelled:
		m.modal = m.reopenLoginModal(v.Email)
		return m, m.modal.Init(), true

	case verify.Cancelled:
		m.modal = m.reopenLoginModal(v.Email)
		return m, m.modal.Init(), true

	case api.RegisterSucceeded:
		// A pending account exists (fresh or re-registered) and a PIN is on
		// its way; the verify modal takes over.
		m.modal = m.openVerifyModal(v.Email)
		return m, m.modal.Init(), true

	case api.VerifySucceeded:
		// The account is active; back to sign-in with the email prefilled so
		// only the password is left to type.
		m.modal = login.New(m.loginSubmitter(), m.server.String()).
			PrefillEmail(v.Email).
			ShowNotice("Account verified — sign in")
		return m, m.modal.Init(), true

	case api.WSAuthRejected:
		if v.Generation != m.sessionGeneration {
			return m, nil, true
		}
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd, true
		}
		return m, nil, true

	case api.WSDialFailed:
		if v.Generation != m.sessionGeneration {
			return m, nil, true
		}
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd, true
		}
		return m, nil, true

	case api.WSAuthTimedOut:
		if v.Generation != m.sessionGeneration {
			return m, nil, true
		}
		if m.modal != nil {
			nextModal, cmd := m.modal.Update(msg)
			m.modal = nextModal
			return m, cmd, true
		}
		return m, nil, true

	case api.WSDisconnected:
		if v.Generation != m.sessionGeneration {
			return m, nil, true
		}
		var previousEmail string
		m, previousEmail = m.resetSession()
		m.modal = m.openLoginModalWithError("Connection lost — please sign in again", previousEmail)
		return m, m.modal.Init(), true
	}
	return m, nil, false
}

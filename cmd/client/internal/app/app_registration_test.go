package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/login"
	"github.com/oklahomer/blabby/cmd/client/internal/register"
	"github.com/oklahomer/blabby/cmd/client/internal/verify"
)

func TestCtrlNFromLoginOpensRegisterModal(t *testing.T) {
	m := makeModel(t)

	// The login modal absorbs the key and reports the outcome as a typed
	// message; the root maps that message to the register modal.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if cmd == nil {
		t.Fatal("expected a CreateAccountRequested cmd from ctrl+n")
	}
	next, _ = next.(Model).Update(cmd())
	if _, ok := next.(Model).modal.(register.Model); !ok {
		t.Fatalf("modal = %T, want register.Model", next.(Model).modal)
	}
}

func TestRegisterCancelledReturnsToLogin(t *testing.T) {
	m := makeModel(t)
	m.modal = register.New(m.registerSubmitter(), m.server.String())

	next, _ := m.Update(register.Cancelled{})
	if _, ok := next.(Model).modal.(login.Model); !ok {
		t.Fatalf("modal = %T, want login.Model", next.(Model).modal)
	}
}

func TestRegisterSucceededOpensVerifyModal(t *testing.T) {
	m := makeModel(t)
	m.modal = register.New(m.registerSubmitter(), m.server.String())

	next, _ := m.Update(api.RegisterSucceeded{Email: "dana@example.com"})
	got := next.(Model)
	if _, ok := got.modal.(verify.Model); !ok {
		t.Fatalf("modal = %T, want verify.Model", got.modal)
	}
	if view := got.modal.View(80, 24); !strings.Contains(view, "dana@example.com") {
		t.Fatalf("verify modal does not show the registered email: %s", view)
	}
}

func TestVerifySucceededReturnsToLoginWithNotice(t *testing.T) {
	m := makeModel(t)
	m.modal = m.openVerifyModal("dana@example.com")

	next, _ := m.Update(api.VerifySucceeded{Email: "dana@example.com"})
	got := next.(Model)
	if _, ok := got.modal.(login.Model); !ok {
		t.Fatalf("modal = %T, want login.Model", got.modal)
	}
	view := got.modal.View(80, 24)
	if !strings.Contains(view, "Account verified") {
		t.Fatalf("success notice missing: %s", view)
	}
	if !strings.Contains(view, "dana@example.com") {
		t.Fatalf("email not prefilled: %s", view)
	}
}

func TestVerifyCancelledReturnsToLogin(t *testing.T) {
	m := makeModel(t)
	m.modal = m.openVerifyModal("dana@example.com")

	next, _ := m.Update(verify.Cancelled{})
	if _, ok := next.(Model).modal.(login.Model); !ok {
		t.Fatalf("modal = %T, want login.Model", next.(Model).modal)
	}
}

func TestPendingLoginRejectionRoutesToVerifyModal(t *testing.T) {
	m := makeModel(t)

	next, _ := m.Update(api.LoginRejected{
		Status: api.StatusAccountPending, Email: "dana@example.com", HTTPStatus: 401,
	})
	got := next.(Model)
	if _, ok := got.modal.(verify.Model); !ok {
		t.Fatalf("modal = %T, want verify.Model", got.modal)
	}
	if view := got.modal.View(80, 24); !strings.Contains(view, "dana@example.com") {
		t.Fatalf("verify modal does not show the attempted email: %s", view)
	}
}

func TestNonPendingLoginRejectionStaysOnLogin(t *testing.T) {
	m := makeModel(t)

	next, _ := m.Update(api.LoginRejected{Status: "AUTH_INVALID_TOKEN", HTTPStatus: 401})
	got := next.(Model)
	if _, ok := got.modal.(login.Model); !ok {
		t.Fatalf("modal = %T, want login.Model", got.modal)
	}
	if view := got.modal.View(80, 24); !strings.Contains(view, "Invalid credentials") {
		t.Fatalf("rejection headline missing: %s", view)
	}
}

func TestWSAuthSucceededWithNonLoginModalIsDropped(t *testing.T) {
	// A same-generation auth completion arriving while a non-login modal is
	// open (or none at all) is stale; it must not clobber the open modal or
	// re-initialise the session state.
	m := makeModel(t)
	m.modal = register.New(m.registerSubmitter(), m.server.String())

	next, cmd := m.Update(api.WSAuthSucceeded{UserID: "u-1"})
	got := next.(Model)
	if cmd != nil {
		t.Fatal("stale auth completion must not dispatch cmds")
	}
	if _, ok := got.modal.(register.Model); !ok {
		t.Fatalf("modal = %T, want the register modal untouched", got.modal)
	}
	if got.connected {
		t.Fatal("stale auth completion must not mark the session connected")
	}
}

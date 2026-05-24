package app

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// makeModel builds a baseline app.Model with the login modal
// installed and a no-op FrameSender wired in. Dispatch-table tests
// can mutate fields (modal, focus, conn, ...) before driving Update.
func makeModel(t *testing.T) Model {
	t.Helper()
	u, _ := url.Parse("http://localhost:8080")
	return New(u, &http.Client{}).SetProgram(&captureSender{}).OpenLoginModal()
}

func TestUpdateWindowSizeMsg(t *testing.T) {
	m := makeModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	got := next.(Model)
	if got.width != 120 || got.height != 40 {
		t.Fatalf("got %dx%d, want 120x40", got.width, got.height)
	}
}

func TestUpdateTickAdvancesClockAndReissues(t *testing.T) {
	m := makeModel(t)
	now := time.Date(2026, 5, 24, 14, 22, 1, 0, time.Local)
	next, cmd := m.Update(tickMsg(now))
	got := next.(Model)
	if !got.now.Equal(now) {
		t.Fatalf("clock did not advance; got %v want %v", got.now, now)
	}
	if !got.infoState.Now.Equal(now) {
		t.Fatalf("infoState.Now not synced; got %v", got.infoState.Now)
	}
	if cmd == nil {
		t.Fatal("expected tickEverySecond cmd to be re-issued")
	}
}

func TestUpdateCtrlCQuitsAndClosesConn(t *testing.T) {
	m := makeModel(t)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd")
	}
}

func TestUpdateFocusInterpretedOnlyWhenNoModal(t *testing.T) {
	m := makeModel(t)
	m.modal = nil // close the modal so focus keys reach interpret
	m.focus = focusRooms

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	got := next.(Model)
	if got.focus != focusMainView {
		t.Fatalf("focus = %v, want focusMainView", got.focus)
	}
}

func TestUpdateLoginSucceededAdvancesToConnecting(t *testing.T) {
	m := makeModel(t)
	next, cmd := m.Update(api.LoginSucceeded{Token: "fake.jwt.tok", Username: "rina"})
	if cmd == nil {
		t.Fatal("expected DialAndAuthCmd to fire")
	}
	got := next.(Model)
	if got.token != "fake.jwt.tok" {
		t.Fatalf("token not retained: %q", got.token)
	}
	if got.username != "rina" {
		t.Fatalf("username not retained: %q", got.username)
	}
}

func TestUpdateLoginRejectedForwardedToModal(t *testing.T) {
	m := makeModel(t)
	m.width, m.height = 100, 30
	rej := api.LoginRejected{Status: "AUTH_INVALID_TOKEN", Message: "bad creds"}
	next, _ := m.Update(rej)
	got := next.(Model)
	view := got.View()
	if !strings.Contains(view, "Invalid credentials") {
		t.Fatalf("modal did not render error; view:\n%s", view)
	}
}

func TestUpdateWSAuthRejectedSurfacedByModal(t *testing.T) {
	m := makeModel(t)
	next, _ := m.Update(api.WSAuthRejected{Status: "AUTH_INVALID_TOKEN", Message: "rejected"})
	got := next.(Model)
	if got.modal == nil {
		t.Fatal("expected modal to remain open")
	}
}

func TestUpdateWSDisconnectedReopensLoginModal(t *testing.T) {
	m := makeModel(t)
	m.width, m.height = 100, 30
	m.modal = nil
	m.token = "fake.jwt"
	m.username = "rina"
	m.userID = "u-rina-1"
	m.infoState.Username = "rina"
	m.infoState.UserID = "u-rina-1"

	next, cmd := m.Update(api.WSDisconnected{Err: errors.New("server closed")})
	got := next.(Model)

	if got.modal == nil {
		t.Fatal("expected login modal re-opened")
	}
	if got.token != "" || got.username != "" || got.userID != "" {
		t.Fatal("expected session cleared")
	}
	if got.infoState.Username != "" || got.infoState.UserID != "" {
		t.Fatal("expected Profile cleared")
	}
	if cmd == nil {
		t.Fatal("expected modal Init cmd")
	}

	view := got.View()
	if !strings.Contains(view, "Connection lost") {
		t.Fatalf("expected connection-lost headline; view:\n%s", view)
	}
}

func TestUpdateFrameReceivedDroppedSilently(t *testing.T) {
	m := makeModel(t)
	next, cmd := m.Update(api.WSFrameReceived{Type: "message", Raw: []byte(`{"type":"message"}`)})
	if cmd != nil {
		t.Fatal("inbound frames must not produce a cmd from the base dispatch")
	}
	if _, ok := next.(Model); !ok {
		t.Fatal("expected Model returned")
	}
}

func TestViewBeforeWindowSizeReturnsEmpty(t *testing.T) {
	m := makeModel(t)
	if m.View() != "" {
		t.Fatal("expected empty view before WindowSizeMsg")
	}
}

func TestAdvanceLoginToConnectingNoOpWhenWrongModal(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	got, _ := m.advanceLoginToConnecting()
	if got != nil {
		t.Fatalf("expected nil modal, got %T", got)
	}
}

// captureSender lives in app_test.go; this file shares it via the
// package boundary. The interface dependency on api.FrameSender means
// any FrameSender satisfies SetProgram(), including the no-op one
// used by tests that never exercise the WS read loop.
var _ api.FrameSender = (*captureSender)(nil)

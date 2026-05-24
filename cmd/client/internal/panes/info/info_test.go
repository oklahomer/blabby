package info

import (
	"strings"
	"testing"
	"time"
)

func TestViewPreAuthShowsServerOnly(t *testing.T) {
	out := View(State{Server: "http://localhost:8080", Now: time.Date(2026, 5, 24, 14, 22, 1, 0, time.UTC)}, false, 25, 20)
	if !strings.Contains(out, "Profile") {
		t.Errorf("missing Profile header:\n%s", out)
	}
	if !strings.Contains(out, "http://localhost:8080") {
		t.Errorf("missing server:\n%s", out)
	}
	if !strings.Contains(out, "14:22:01") {
		t.Errorf("missing clock:\n%s", out)
	}
	if !strings.Contains(out, "2026-05-24") {
		t.Errorf("missing date:\n%s", out)
	}
}

func TestViewPostAuthShowsUsernameAndID(t *testing.T) {
	out := View(State{
		Username: "rina",
		UserID:   "u-rina-1",
		Server:   "http://localhost:8080",
		Now:      time.Date(2026, 5, 24, 14, 22, 3, 0, time.UTC),
	}, false, 25, 20)
	if !strings.Contains(out, "rina") {
		t.Errorf("missing username:\n%s", out)
	}
	if !strings.Contains(out, "u-rina-1") {
		t.Errorf("missing user id:\n%s", out)
	}
}

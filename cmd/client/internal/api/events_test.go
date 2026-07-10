package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func userRef(id, name string) *UserRef { return &UserRef{ID: id, Name: name} }

func strPtr(s string) *string { return &s }

func TestLoadEventsCmdSuccess(t *testing.T) {
	t.Parallel()
	const ms int64 = 1_700_000_000_000
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rooms/general/events" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testBearerToken {
			t.Errorf("missing/incorrect bearer header: %q", got)
		}
		if got := r.URL.Query().Get("before"); got != "" {
			t.Errorf("newest-page request must not send before, got %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "" {
			t.Errorf("LoadEventsCmd must not send a limit, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RoomEventsResponse{
			Events: []RoomEvent{
				{ID: "100", Type: "message", Sender: userRef("U1", "Alice"), Text: "hi", Timestamp: ms},
				{ID: "99", Type: "member_joined", User: userRef("U2", "Bob"), Timestamp: ms},
				{ID: "98", Type: "member_left", User: userRef("U2", "Bob"), Timestamp: 0},
			},
			Next: strPtr("98"),
		})
	}))
	defer srv.Close()

	msg := LoadEventsCmd(srv.Client(), srv.URL, testBearerToken, "general", "", testRoomGeneration, time.Second)()
	got, ok := msg.(RoomEventsLoaded)
	if !ok {
		t.Fatalf("expected RoomEventsLoaded, got %T: %#v", msg, msg)
	}
	if got.RoomID != "general" || got.Next != "98" || got.Before != "" {
		t.Fatalf("envelope fields wrong: %#v", got)
	}
	if got.Generation != testRoomGeneration {
		t.Fatalf("Generation = %d, want %d", got.Generation, testRoomGeneration)
	}
	if len(got.Events) != 3 {
		t.Fatalf("expected 3 events, got %d: %#v", len(got.Events), got.Events)
	}
	if got.Events[0].Kind != TimelineMessage || got.Events[0].EventID != 100 ||
		got.Events[0].Person.Name != "Alice" || got.Events[0].Text != "hi" {
		t.Fatalf("event 0 wrong: %#v", got.Events[0])
	}
	if !got.Events[0].At.Equal(time.UnixMilli(ms)) {
		t.Fatalf("event 0 At = %v, want %v", got.Events[0].At, time.UnixMilli(ms))
	}
	if got.Events[1].Kind != TimelineJoined || got.Events[1].EventID != 99 || got.Events[1].Person.ID != "U2" {
		t.Fatalf("event 1 wrong: %#v", got.Events[1])
	}
	if got.Events[2].Kind != TimelineLeft || !got.Events[2].At.IsZero() {
		t.Fatalf("event 2 wrong: %#v", got.Events[2])
	}
}

func TestLoadEventsCmdBeforeCursorEchoedAndSent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("before"); got != "500" {
			t.Errorf("before param = %q, want 500", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RoomEventsResponse{Events: []RoomEvent{}, Next: nil})
	}))
	defer srv.Close()

	msg := LoadEventsCmd(srv.Client(), srv.URL, testBearerToken, "general", "500", testRoomGeneration, time.Second)()
	got, ok := msg.(RoomEventsLoaded)
	if !ok {
		t.Fatalf("expected RoomEventsLoaded, got %T", msg)
	}
	if got.Before != "500" {
		t.Fatalf("Before = %q, want 500", got.Before)
	}
	if got.Next != "" {
		t.Fatalf("Next = %q, want empty for a null cursor", got.Next)
	}
	if len(got.Events) != 0 {
		t.Fatalf("expected empty page, got %#v", got.Events)
	}
}

func TestLoadEventsCmdUnknownKindSkipped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[` +
			`{"id":"3","type":"reaction","user":{"id":"U1"},"timestamp":1},` +
			`{"id":"2","type":"message","sender":{"id":"U1","name":"A"},"text":"hi","timestamp":1}` +
			`],"next":null}`))
	}))
	defer srv.Close()

	msg := LoadEventsCmd(srv.Client(), srv.URL, testBearerToken, "general", "", testRoomGeneration, time.Second)()
	got, ok := msg.(RoomEventsLoaded)
	if !ok {
		t.Fatalf("expected RoomEventsLoaded, got %T: %#v", msg, msg)
	}
	if len(got.Events) != 1 || got.Events[0].EventID != 2 {
		t.Fatalf("unknown kind not skipped: %#v", got.Events)
	}
}

func TestLoadEventsCmdMalformedIDFailsPage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[{"id":"nope","type":"message","sender":{"id":"U1"},"text":"hi","timestamp":1}],"next":null}`))
	}))
	defer srv.Close()

	msg := LoadEventsCmd(srv.Client(), srv.URL, testBearerToken, "general", "", testRoomGeneration, time.Second)()
	got, ok := msg.(RoomEventsLoadFailed)
	if !ok {
		t.Fatalf("expected RoomEventsLoadFailed, got %T: %#v", msg, msg)
	}
	if got.HTTPStatus != http.StatusOK || got.Message == "" {
		t.Fatalf("expected a decode failure on a 200 body, got %#v", got)
	}
}

func TestLoadEventsCmdMissingPersonFailsPage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[{"id":"5","type":"member_joined","timestamp":1}],"next":null}`))
	}))
	defer srv.Close()

	msg := LoadEventsCmd(srv.Client(), srv.URL, testBearerToken, "general", "", testRoomGeneration, time.Second)()
	if _, ok := msg.(RoomEventsLoadFailed); !ok {
		t.Fatalf("expected RoomEventsLoadFailed for a member event without a user, got %T", msg)
	}
}

func TestLoadEventsCmdNotMember(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 2001, Status: "ROOM_NOT_MEMBER", Message: "not a member",
		}})
	}))
	defer srv.Close()

	msg := LoadEventsCmd(srv.Client(), srv.URL, testBearerToken, "general", "", testRoomGeneration, time.Second)()
	got, ok := msg.(RoomEventsLoadFailed)
	if !ok {
		t.Fatalf("expected RoomEventsLoadFailed, got %T", msg)
	}
	if got.Status != "ROOM_NOT_MEMBER" || got.HTTPStatus != http.StatusForbidden {
		t.Fatalf("got %#v", got)
	}
	if got.RoomID != "general" {
		t.Fatalf("RoomID not preserved: %q", got.RoomID)
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

func TestLoadEventsCmdUnauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 1002, Status: "AUTH_EXPIRED_TOKEN", Message: "token expired",
		}})
	}))
	defer srv.Close()

	msg := LoadEventsCmd(srv.Client(), srv.URL, testBearerToken, "general", "", testRoomGeneration, time.Second)()
	got, ok := msg.(RoomEventsLoadFailed)
	if !ok {
		t.Fatalf("expected RoomEventsLoadFailed, got %T", msg)
	}
	if got.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("HTTPStatus = %d, want 401", got.HTTPStatus)
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

func TestLoadEventsCmdTransportError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	msg := LoadEventsCmd(&http.Client{}, addr, testBearerToken, "general", "", testRoomGeneration, 250*time.Millisecond)()
	got, ok := msg.(RoomEventsLoadFailed)
	if !ok {
		t.Fatalf("expected RoomEventsLoadFailed, got %T", msg)
	}
	if got.HTTPStatus != 0 {
		t.Fatalf("expected HTTPStatus 0 for transport error, got %d", got.HTTPStatus)
	}
	if got.Message == "" {
		t.Fatal("expected non-empty Message describing the transport failure")
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

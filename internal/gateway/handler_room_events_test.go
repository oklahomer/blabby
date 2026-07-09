package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
)

// serveEvents dispatches GET /rooms/{id}/events through a one-route mux so
// PathValue captures {id} the same way the production mux does.
func serveEvents(t *testing.T, g *Gateway, path, userID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rooms/{id}/events", g.handleRoomEvents)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = withUserContext(t, req, userID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// timelineEntry builds one persistence.TimelineEntry fixture with a fixed timestamp so
// exact-body assertions are deterministic.
func timelineEntry(t *testing.T, eid int64, kind persistence.TimelineEntryKind, code, name, text string) persistence.TimelineEntry {
	t.Helper()
	eventID, err := id.NewEventID(eid)
	if err != nil {
		t.Fatalf("NewEventID(%d): %v", eid, err)
	}
	parsed, err := id.ParsePublicCode(code)
	if err != nil {
		t.Fatalf("ParsePublicCode(%q): %v", code, err)
	}
	return persistence.TimelineEntry{
		ID:         eventID,
		Kind:       kind,
		User:       persistence.TimelineUser{Code: parsed, Name: name},
		Text:       text,
		OccurredAt: time.UnixMilli(1_700_000_000_000),
	}
}

// eventsGateway builds a gateway whose timeline stub holds the given room-4
// history (newest-first) with user 1 as the sole member.
func eventsGateway(entries ...persistence.TimelineEntry) (*Gateway, *stubRoomTimeline) {
	g := gatewayWithFake(&fakeUserGrainCaller{})
	stub := newStubRoomTimeline()
	stub.entries[4] = entries
	g.timeline = stub
	return g, stub
}

func TestHandleRoomEvents_ReturnsInterleavedPageAsCodes(t *testing.T) {
	g, _ := eventsGateway(
		timelineEntry(t, 102, persistence.EntryMessage, "A000000001", "alice", "hello 世界"),
		timelineEntry(t, 101, persistence.EntryMemberJoined, "B000000002", "bob", ""),
	)
	rec := serveEvents(t, g, "/rooms/RG000000004/events", "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	// Messages carry sender+text; membership entries carry user — mirroring the
	// WS frames. Authors render as U… codes, never internal ids; event ids are
	// decimal strings; next is an explicit null when the listing is exhausted.
	want := `{"events":[` +
		`{"id":"102","type":"message","sender":{"id":"UA000000001","name":"alice"},"text":"hello 世界","timestamp":1700000000000},` +
		`{"id":"101","type":"member_joined","user":{"id":"UB000000002","name":"bob"},"timestamp":1700000000000}` +
		`],"next":null}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestHandleRoomEvents_EmptyPageMarshalsAsArrayNotNull(t *testing.T) {
	g, _ := eventsGateway()
	rec := serveEvents(t, g, "/rooms/RG000000004/events", "1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"events":[],"next":null}` {
		t.Errorf("body = %s, want empty events array with null next", got)
	}
}

func TestHandleRoomEvents_PagesWithLimitAndBefore(t *testing.T) {
	g, _ := eventsGateway(
		timelineEntry(t, 103, persistence.EntryMessage, "A000000001", "alice", "three"),
		timelineEntry(t, 102, persistence.EntryMessage, "A000000001", "alice", "two"),
		timelineEntry(t, 101, persistence.EntryMessage, "A000000001", "alice", "one"),
	)

	rec := serveEvents(t, g, "/rooms/RG000000004/events?limit=2", "1")
	if rec.Code != http.StatusOK {
		t.Fatalf("page 1 status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"next":"102"`) || !strings.Contains(body, `"id":"103"`) {
		t.Fatalf("page 1 = %s, want ids 103..102 with next=102", body)
	}

	rec = serveEvents(t, g, "/rooms/RG000000004/events?limit=2&before=102", "1")
	if rec.Code != http.StatusOK {
		t.Fatalf("page 2 status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, `"id":"101"`) || !strings.Contains(body, `"next":null`) {
		t.Fatalf("page 2 = %s, want just id 101 with null next", body)
	}
}

func TestHandleRoomEvents_FiltersMessagesByQuery(t *testing.T) {
	g, _ := eventsGateway(
		timelineEntry(t, 103, persistence.EntryMemberJoined, "B000000002", "bob", ""),
		timelineEntry(t, 102, persistence.EntryMessage, "A000000001", "alice", "standup notes"),
		timelineEntry(t, 101, persistence.EntryMessage, "A000000001", "alice", "random chat"),
	)
	rec := serveEvents(t, g, "/rooms/RG000000004/events?q=standup", "1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"102"`) || strings.Contains(body, `"id":"101"`) || strings.Contains(body, `"id":"103"`) {
		t.Errorf("body = %s, want only the matching message", body)
	}
}

func TestHandleRoomEvents_NonMemberReturns403(t *testing.T) {
	g, _ := eventsGateway()
	rec := serveEvents(t, g, "/rooms/RG000000004/events", "3")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	resp := decodeErrorResponse(t, rec.Body)
	if resp.Error.Code != errcode.RoomNotMember {
		t.Errorf("error.code = %d, want %d", resp.Error.Code, errcode.RoomNotMember)
	}
}

func TestHandleRoomEvents_UnknownRoomReturns404(t *testing.T) {
	g, _ := eventsGateway()
	rec := serveEvents(t, g, "/rooms/RZZZZZZZZZZ/events", "1")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestHandleRoomEvents_RejectsInvalidParams(t *testing.T) {
	g, _ := eventsGateway()
	cases := map[string]string{
		"q over max bytes":  "/rooms/RG000000004/events?q=" + strings.Repeat("a", 257),
		"before not an id":  "/rooms/RG000000004/events?before=abc",
		"before zero":       "/rooms/RG000000004/events?before=0",
		"limit zero":        "/rooms/RG000000004/events?limit=0",
		"limit not integer": "/rooms/RG000000004/events?limit=abc",
		"limit over cap":    "/rooms/RG000000004/events?limit=201",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			rec := serveEvents(t, g, path, "1")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			resp := decodeErrorResponse(t, rec.Body)
			if resp.Error.Code != errcode.InvalidRequest {
				t.Errorf("error.code = %d, want %d", resp.Error.Code, errcode.InvalidRequest)
			}
		})
	}
}

func TestHandleRoomEvents_UnknownEntryKindReturns500(t *testing.T) {
	// The journal is contracted to return known entry kinds; an unknown one
	// means a kind was added without its wire mapping, so the handler fails
	// closed rather than emit an untyped event.
	g, _ := eventsGateway(timelineEntry(t, 101, persistence.TimelineEntryKind(99), "A000000001", "alice", ""))
	rec := serveEvents(t, g, "/rooms/RG000000004/events", "1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
	resp := decodeErrorResponse(t, rec.Body)
	if resp.Error.Code != errcode.InternalError {
		t.Errorf("error.code = %d, want %d", resp.Error.Code, errcode.InternalError)
	}
}

func TestHandleRoomEvents_MembershipCheckErrorReturns503(t *testing.T) {
	g, stub := eventsGateway()
	stub.memberErr = errors.New("db down")
	rec := serveEvents(t, g, "/rooms/RG000000004/events", "1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	resp := decodeErrorResponse(t, rec.Body)
	if strings.Contains(resp.Error.Message, "db down") {
		t.Errorf("error.message leaks underlying error: %q", resp.Error.Message)
	}
}

func TestHandleRoomEvents_ReadErrorReturns503(t *testing.T) {
	g, stub := eventsGateway()
	stub.eventsErr = errors.New("db down")
	rec := serveEvents(t, g, "/rooms/RG000000004/events", "1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleRoomEvents_RejectsMissingAuthContext(t *testing.T) {
	g, _ := eventsGateway()
	rec := serveEvents(t, g, "/rooms/RG000000004/events", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

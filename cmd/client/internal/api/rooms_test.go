package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testBearerToken = "super.secret.jwt"

// rejectsToken returns true when v's marshalled form contains the
// token string anywhere. Used as a literal-leak guard on every
// non-success outcome so a future regression cannot ship a Msg that
// echoes the JWT back into the UI.
func rejectsToken(t *testing.T, v any) bool {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		// Fall back to fmt-stringification so unmarshallable values
		// (e.g. ones holding an error interface) are still scanned.
		return !strings.Contains(reflect.ValueOf(v).String(), testBearerToken)
	}
	return !strings.Contains(string(raw), testBearerToken)
}

func TestLoadJoinedRoomsCmdSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rooms/joined" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testBearerToken {
			t.Errorf("missing/incorrect bearer header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RoomListResponse{Rooms: []Room{
			{ID: "RG000000004", Name: "General"},
			{ID: "RH000000005", Name: "Random"},
		}})
	}))
	defer srv.Close()

	msg := LoadJoinedRoomsCmd(srv.Client(), srv.URL, testBearerToken, time.Second)()
	got, ok := msg.(JoinedRoomsLoaded)
	if !ok {
		t.Fatalf("expected JoinedRoomsLoaded, got %T: %#v", msg, msg)
	}
	want := []Room{{ID: "RG000000004", Name: "General"}, {ID: "RH000000005", Name: "Random"}}
	if !reflect.DeepEqual(got.Rooms, want) {
		t.Fatalf("got rooms %#v, want %#v", got.Rooms, want)
	}
}

func TestLoadJoinedRoomsCmdEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rooms":[]}`))
	}))
	defer srv.Close()

	msg := LoadJoinedRoomsCmd(srv.Client(), srv.URL, testBearerToken, time.Second)()
	got, ok := msg.(JoinedRoomsLoaded)
	if !ok {
		t.Fatalf("expected JoinedRoomsLoaded, got %T", msg)
	}
	if len(got.Rooms) != 0 {
		t.Fatalf("expected empty list, got %#v", got.Rooms)
	}
}

func TestLoadJoinedRoomsCmdUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 1002, Status: "AUTH_EXPIRED_TOKEN", Message: "token expired",
		}})
	}))
	defer srv.Close()

	msg := LoadJoinedRoomsCmd(srv.Client(), srv.URL, testBearerToken, time.Second)()
	got, ok := msg.(JoinedRoomsLoadFailed)
	if !ok {
		t.Fatalf("expected JoinedRoomsLoadFailed, got %T", msg)
	}
	if got.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("got http status %d", got.HTTPStatus)
	}
	if got.Status != "AUTH_EXPIRED_TOKEN" {
		t.Fatalf("got status %q", got.Status)
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked into JoinedRoomsLoadFailed: %#v", got)
	}
}

func TestLoadJoinedRoomsCmdServiceUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 5002, Status: "SERVICE_UNAVAILABLE", Message: "failed to reach user grain",
		}})
	}))
	defer srv.Close()

	msg := LoadJoinedRoomsCmd(srv.Client(), srv.URL, testBearerToken, time.Second)()
	got, ok := msg.(JoinedRoomsLoadFailed)
	if !ok {
		t.Fatalf("expected JoinedRoomsLoadFailed, got %T", msg)
	}
	if got.Status != "SERVICE_UNAVAILABLE" {
		t.Fatalf("got status %q", got.Status)
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

func TestLoadJoinedRoomsCmdMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()

	msg := LoadJoinedRoomsCmd(srv.Client(), srv.URL, testBearerToken, time.Second)()
	got, ok := msg.(JoinedRoomsLoadFailed)
	if !ok {
		t.Fatalf("expected JoinedRoomsLoadFailed, got %T", msg)
	}
	if got.HTTPStatus != http.StatusOK {
		t.Fatalf("expected HTTPStatus 200 echoed, got %d", got.HTTPStatus)
	}
	if got.Message == "" {
		t.Fatal("expected non-empty Message describing the decode failure")
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

func TestLoadJoinedRoomsCmdTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	msg := LoadJoinedRoomsCmd(&http.Client{}, addr, testBearerToken, 250*time.Millisecond)()
	got, ok := msg.(JoinedRoomsLoadFailed)
	if !ok {
		t.Fatalf("expected JoinedRoomsLoadFailed, got %T", msg)
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

func TestLoadRoomsCmdSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rooms" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testBearerToken {
			t.Errorf("missing/incorrect bearer header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RoomListResponse{Rooms: []Room{
			{ID: "general", Name: "General"},
			{ID: "random", Name: "Random"},
		}})
	}))
	defer srv.Close()

	msg := LoadRoomsCmd(srv.Client(), srv.URL, testBearerToken, RoomQuery{}, time.Second)()
	got, ok := msg.(RoomsLoaded)
	if !ok {
		t.Fatalf("expected RoomsLoaded, got %T", msg)
	}
	if len(got.Rooms) != 2 || got.Rooms[0].ID != "general" || got.Rooms[1].Name != "Random" {
		t.Fatalf("unexpected catalogue: %#v", got.Rooms)
	}
}

func TestLoadRoomsCmdRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 5002, Status: "SERVICE_UNAVAILABLE", Message: "down",
		}})
	}))
	defer srv.Close()

	msg := LoadRoomsCmd(srv.Client(), srv.URL, testBearerToken, RoomQuery{}, time.Second)()
	got, ok := msg.(RoomsLoadFailed)
	if !ok {
		t.Fatalf("expected RoomsLoadFailed, got %T", msg)
	}
	if got.Status != "SERVICE_UNAVAILABLE" || got.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("got %#v", got)
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

func TestLoadRoomsCmdEnvelopeWithoutStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`Bad Gateway`))
	}))
	defer srv.Close()

	msg := LoadRoomsCmd(srv.Client(), srv.URL, testBearerToken, RoomQuery{}, time.Second)()
	got, ok := msg.(RoomsLoadFailed)
	if !ok {
		t.Fatalf("expected RoomsLoadFailed, got %T", msg)
	}
	if got.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("got http status %d", got.HTTPStatus)
	}
	if got.Status != "" {
		t.Fatalf("expected empty Status, got %q", got.Status)
	}
	if got.Message == "" {
		t.Fatal("expected non-empty fallback Message")
	}
}

func TestLoadRoomsCmdEncodesQueryAndCursorAndEchoesNext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "gen 話" {
			t.Errorf("q = %q, want the raw fragment", got)
		}
		if got := r.URL.Query().Get("after"); got != "RG000000004" {
			t.Errorf("after = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		next := "RK000000042"
		_ = json.NewEncoder(w).Encode(RoomListResponse{
			Rooms: []Room{{ID: "RK000000042", Name: "General 2"}},
			Next:  &next,
		})
	}))
	defer srv.Close()

	query := RoomQuery{Query: "gen 話", After: "RG000000004"}
	msg := LoadRoomsCmd(srv.Client(), srv.URL, testBearerToken, query, time.Second)()
	got, ok := msg.(RoomsLoaded)
	if !ok {
		t.Fatalf("expected RoomsLoaded, got %T", msg)
	}
	if got.Next != "RK000000042" {
		t.Fatalf("Next = %q, want the server cursor", got.Next)
	}
	// The dispatched parameters echo back so the modal can drop stale results
	// and distinguish append from replace.
	if got.Query != "gen 話" || got.After != "RG000000004" {
		t.Fatalf("echo = %q/%q, want the dispatched query/cursor", got.Query, got.After)
	}
}

func TestLoadRoomsCmdNullNextIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("zero RoomQuery must add no parameters, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rooms":[],"next":null}`))
	}))
	defer srv.Close()

	msg := LoadRoomsCmd(srv.Client(), srv.URL, testBearerToken, RoomQuery{}, time.Second)()
	got, ok := msg.(RoomsLoaded)
	if !ok {
		t.Fatalf("expected RoomsLoaded, got %T", msg)
	}
	if got.Next != "" {
		t.Fatalf("Next = %q, want empty for a null cursor", got.Next)
	}
}

func TestCreateRoomCmd(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/rooms" {
				http.Error(w, "wrong route", http.StatusNotFound)
				return
			}
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if req["name"] != "Team Standup" {
				t.Errorf("name = %q", req["name"])
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Room{ID: "RK000000042", Name: "Team Standup"})
		}))
		defer srv.Close()

		msg := CreateRoomCmd(srv.Client(), srv.URL, testBearerToken, "Team Standup", time.Second)()
		got, ok := msg.(RoomCreated)
		if !ok {
			t.Fatalf("expected RoomCreated, got %T", msg)
		}
		if got.Room.ID != "RK000000042" || got.Room.Name != "Team Standup" {
			t.Fatalf("room = %#v", got.Room)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
				Code: 4001, Status: "INVALID_REQUEST", Message: "name must be 1-64 bytes of printable characters",
			}})
		}))
		defer srv.Close()

		msg := CreateRoomCmd(srv.Client(), srv.URL, testBearerToken, "\x01", time.Second)()
		got, ok := msg.(RoomCreateFailed)
		if !ok {
			t.Fatalf("expected RoomCreateFailed, got %T", msg)
		}
		if got.Status != "INVALID_REQUEST" || got.HTTPStatus != http.StatusBadRequest {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		addr := srv.URL
		srv.Close()

		msg := CreateRoomCmd(&http.Client{}, addr, testBearerToken, "Team Standup", time.Second)()
		got, ok := msg.(RoomCreateFailed)
		if !ok {
			t.Fatalf("expected RoomCreateFailed, got %T", msg)
		}
		if got.HTTPStatus != 0 {
			t.Fatalf("transport failure must carry HTTPStatus 0, got %d", got.HTTPStatus)
		}
	})
}

func TestLeaveRoomCmd(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != "/rooms/general/membership" {
				http.Error(w, "wrong route", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(JoinSuccessResponse{Success: true})
		}))
		defer srv.Close()

		msg := LeaveRoomCmd(srv.Client(), srv.URL, testBearerToken, "general", "General", time.Second)()
		got, ok := msg.(RoomLeft)
		if !ok {
			t.Fatalf("expected RoomLeft, got %T", msg)
		}
		if got.RoomID != "general" || got.RoomName != "General" {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("owner cannot leave", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
				Code: 2004, Status: "ROOM_OWNER_CANNOT_LEAVE", Message: "transfer ownership before leaving the room",
			}})
		}))
		defer srv.Close()

		msg := LeaveRoomCmd(srv.Client(), srv.URL, testBearerToken, "general", "General", time.Second)()
		got, ok := msg.(RoomLeaveFailed)
		if !ok {
			t.Fatalf("expected RoomLeaveFailed, got %T", msg)
		}
		if got.Status != "ROOM_OWNER_CANNOT_LEAVE" || got.RoomID != "general" {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		addr := srv.URL
		srv.Close()

		msg := LeaveRoomCmd(&http.Client{}, addr, testBearerToken, "general", "General", time.Second)()
		got, ok := msg.(RoomLeaveFailed)
		if !ok {
			t.Fatalf("expected RoomLeaveFailed, got %T", msg)
		}
		if got.HTTPStatus != 0 || got.RoomID != "general" {
			t.Fatalf("got %#v", got)
		}
	})
}

func TestJoinRoomCmdSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/rooms/general/membership" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testBearerToken {
			t.Errorf("missing/incorrect bearer header: %q", got)
		}
		if r.Body != nil && r.ContentLength > 0 {
			t.Errorf("expected empty membership request body, content length=%d", r.ContentLength)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(JoinSuccessResponse{Success: true})
	}))
	defer srv.Close()

	msg := JoinRoomCmd(srv.Client(), srv.URL, testBearerToken, "general", "General", time.Second)()
	got, ok := msg.(RoomJoined)
	if !ok {
		t.Fatalf("expected RoomJoined, got %T", msg)
	}
	if got.RoomID != "general" || got.RoomName != "General" {
		t.Fatalf("got %#v", got)
	}
}

func TestJoinRoomCmdAlreadyMember(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 2002, Status: "ROOM_ALREADY_MEMBER", Message: "already a member",
		}})
	}))
	defer srv.Close()

	msg := JoinRoomCmd(srv.Client(), srv.URL, testBearerToken, "general", "General", time.Second)()
	got, ok := msg.(RoomJoinFailed)
	if !ok {
		t.Fatalf("expected RoomJoinFailed, got %T", msg)
	}
	if got.Status != "ROOM_ALREADY_MEMBER" {
		t.Fatalf("got status %q", got.Status)
	}
	if got.RoomID != "general" {
		t.Fatalf("RoomID not preserved: %q", got.RoomID)
	}
	if got.HTTPStatus != http.StatusConflict {
		t.Fatalf("got http status %d", got.HTTPStatus)
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

func TestJoinRoomCmdNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 2003, Status: "ROOM_NOT_FOUND", Message: "no such room",
		}})
	}))
	defer srv.Close()

	msg := JoinRoomCmd(srv.Client(), srv.URL, testBearerToken, "ghost", "Ghost", time.Second)()
	got, ok := msg.(RoomJoinFailed)
	if !ok {
		t.Fatalf("expected RoomJoinFailed, got %T", msg)
	}
	if got.Status != "ROOM_NOT_FOUND" {
		t.Fatalf("got status %q", got.Status)
	}
	if got.RoomID != "ghost" {
		t.Fatalf("RoomID not preserved: %q", got.RoomID)
	}
}

func TestJoinRoomCmdTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	msg := JoinRoomCmd(&http.Client{}, addr, testBearerToken, "general", "General", 250*time.Millisecond)()
	got, ok := msg.(RoomJoinFailed)
	if !ok {
		t.Fatalf("expected RoomJoinFailed, got %T", msg)
	}
	if got.HTTPStatus != 0 {
		t.Fatalf("expected HTTPStatus 0, got %d", got.HTTPStatus)
	}
	if got.RoomID != "general" {
		t.Fatalf("RoomID not preserved: %q", got.RoomID)
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

// deadlineCapturingTransport records the deadline carried by the
// outgoing request's context. Used to assert that doRoomRequest installs
// DefaultRoomCallTimeout when the caller passes timeout <= 0 — the
// server-side r.Context() does NOT inherit the client deadline through
// net/http, so the inspection has to happen on the transport.
type deadlineCapturingTransport struct {
	inner       http.RoundTripper
	hasDeadline bool
	deadline    time.Time
}

func (d *deadlineCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if dl, ok := req.Context().Deadline(); ok {
		d.hasDeadline = true
		d.deadline = dl
	}
	return d.inner.RoundTrip(req)
}

func TestRoomCmdsZeroTimeoutUsesDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RoomListResponse{Rooms: nil})
	}))
	defer srv.Close()

	transport := &deadlineCapturingTransport{inner: srv.Client().Transport}
	if transport.inner == nil {
		transport.inner = http.DefaultTransport
	}
	client := &http.Client{Transport: transport}

	before := time.Now()
	msg := LoadJoinedRoomsCmd(client, srv.URL, testBearerToken, 0)()
	if _, ok := msg.(JoinedRoomsLoaded); !ok {
		t.Fatalf("expected JoinedRoomsLoaded with default timeout, got %T", msg)
	}
	if !transport.hasDeadline {
		t.Fatal("expected outgoing request to carry a deadline when timeout=0")
	}
	actual := transport.deadline.Sub(before)
	// The default is DefaultRoomCallTimeout; the captured deadline is
	// (before + DefaultRoomCallTimeout), give or take scheduling slack.
	const slack = 500 * time.Millisecond
	if actual < DefaultRoomCallTimeout-slack || actual > DefaultRoomCallTimeout+slack {
		t.Errorf("effective timeout = %v, want within ±%v of %v", actual, slack, DefaultRoomCallTimeout)
	}
}

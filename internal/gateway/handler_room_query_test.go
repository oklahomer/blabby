package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/errcode"
)

func serveQuery(t *testing.T, g *Gateway, pattern, path, userID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	switch pattern {
	case "GET /rooms":
		mux.HandleFunc(pattern, g.handleRoomList)
	case "GET /rooms/joined":
		mux.HandleFunc(pattern, g.handleRoomJoined)
	default:
		t.Fatalf("unsupported pattern: %q", pattern)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = withUserContext(t, req, userID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleRoomList_ReturnsActiveRoomsAsCodes(t *testing.T) {
	g := gatewayWithFake(&fakeUserGrainCaller{})
	rec := serveQuery(t, g, "GET /rooms", "/rooms", "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := strings.TrimSpace(rec.Body.String())
	// The catalogue exposes opaque R… codes, never internal numeric ids.
	want := `{"rooms":[{"id":"RG000000004","name":"General"},{"id":"RH000000005","name":"Random"}]}`
	if got != want {
		t.Errorf("body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestHandleRoomList_RejectsMissingAuthContext(t *testing.T) {
	g := gatewayWithFake(&fakeUserGrainCaller{})
	rec := serveQuery(t, g, "GET /rooms", "/rooms", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for missing auth context", rec.Code)
	}
}

func TestHandleRoomList_DirectoryErrorReturns503(t *testing.T) {
	g := gatewayWithFake(&fakeUserGrainCaller{})
	g.rooms = &stubRoomDirectory{err: errors.New("db down")}
	rec := serveQuery(t, g, "GET /rooms", "/rooms", "1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	resp := decodeErrorResponse(t, rec.Body)
	if strings.Contains(resp.Error.Message, "db down") {
		t.Errorf("error.message leaks underlying error: %q", resp.Error.Message)
	}
}

func TestHandleRoomJoined_EmptySliceMarshalsAsArrayNotNull(t *testing.T) {
	fake := &fakeUserGrainCaller{getJoinedResp: &userpb.GetJoinedRoomsResponse{Rooms: nil}}
	g := gatewayWithFake(fake)
	rec := serveQuery(t, g, "GET /rooms/joined", "/rooms/joined", "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := strings.TrimSpace(rec.Body.String())
	if got != `{"rooms":[]}` {
		t.Errorf("body = %s, want %s", got, `{"rooms":[]}`)
	}
}

func TestHandleRoomJoined_PreservesGrainOrderAsCodes(t *testing.T) {
	// The grain returns refs in an arbitrary order; the gateway renders their
	// public codes directly (no room-repository lookup) and must NOT re-sort.
	fake := &fakeUserGrainCaller{getJoinedResp: &userpb.GetJoinedRoomsResponse{
		Rooms: []*commonpb.RoomRef{
			{RoomId: "5", PublicCode: "H000000005", Name: "Random", Status: "active"},
			{RoomId: "4", PublicCode: "G000000004", Name: "General", Status: "active"},
		},
	}}
	g := gatewayWithFake(fake)
	rec := serveQuery(t, g, "GET /rooms/joined", "/rooms/joined", "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp roomListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Rooms) != 2 ||
		resp.Rooms[0].ID != "RH000000005" || resp.Rooms[0].Name != "Random" ||
		resp.Rooms[1].ID != "RG000000004" || resp.Rooms[1].Name != "General" {
		t.Errorf("rooms = %+v, want [Random(RH…), General(RG…)] (grain order preserved)", resp.Rooms)
	}
}

func TestHandleRoomJoined_MalformedPublicCodeReturns500(t *testing.T) {
	// The User grain is contracted to return well-formed refs; an unparseable
	// public code is a server bug, so the gateway fails closed rather than drop
	// the room silently.
	fake := &fakeUserGrainCaller{getJoinedResp: &userpb.GetJoinedRoomsResponse{
		Rooms: []*commonpb.RoomRef{{RoomId: "4", PublicCode: "not-a-code", Name: "General"}},
	}}
	g := gatewayWithFake(fake)
	rec := serveQuery(t, g, "GET /rooms/joined", "/rooms/joined", "1")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a malformed public code", rec.Code)
	}
}

func TestHandleRoomJoined_TransportErrorReturns503(t *testing.T) {
	fake := &fakeUserGrainCaller{getJoinedErr: errors.New("cluster down")}
	g := gatewayWithFake(fake)
	rec := serveQuery(t, g, "GET /rooms/joined", "/rooms/joined", "1")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	resp := decodeErrorResponse(t, rec.Body)
	if resp.Error.Code != errcode.ServiceUnavailable {
		t.Errorf("error.code = %d, want %d", resp.Error.Code, errcode.ServiceUnavailable)
	}
	// The underlying error must not appear in the gateway-facing message.
	if strings.Contains(resp.Error.Message, "cluster down") {
		t.Errorf("error.message leaks underlying error: %q", resp.Error.Message)
	}
}

func TestHandleRoomJoined_RejectsMissingAuthContext(t *testing.T) {
	g := gatewayWithFake(&fakeUserGrainCaller{})
	rec := serveQuery(t, g, "GET /rooms/joined", "/rooms/joined", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

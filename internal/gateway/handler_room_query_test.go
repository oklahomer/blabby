package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestHandleRoomList_ReturnsDefaultRoomsInOrder(t *testing.T) {
	g := gatewayWithFake(&fakeUserGrainCaller{})
	rec := serveQuery(t, g, "GET /rooms", "/rooms", "alice")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := strings.TrimSpace(rec.Body.String())
	want := `{"rooms":[{"id":"general","name":"General"},{"id":"random","name":"Random"}]}`
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

func TestHandleRoomJoined_EmptySliceMarshalsAsArrayNotNull(t *testing.T) {
	fake := &fakeUserGrainCaller{getJoinedResp: &userpb.GetJoinedRoomsResponse{RoomIds: nil}}
	g := gatewayWithFake(fake)
	rec := serveQuery(t, g, "GET /rooms/joined", "/rooms/joined", "alice")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := strings.TrimSpace(rec.Body.String())
	if got != `{"room_ids":[]}` {
		t.Errorf("body = %s, want %s", got, `{"room_ids":[]}`)
	}
}

func TestHandleRoomJoined_PreservesGrainOrder(t *testing.T) {
	// Grain returns an arbitrary order; gateway must NOT re-sort.
	fake := &fakeUserGrainCaller{getJoinedResp: &userpb.GetJoinedRoomsResponse{
		RoomIds: []string{"random", "general"},
	}}
	g := gatewayWithFake(fake)
	rec := serveQuery(t, g, "GET /rooms/joined", "/rooms/joined", "alice")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp joinedRoomsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.RoomIDs) != 2 || resp.RoomIDs[0] != "random" || resp.RoomIDs[1] != "general" {
		t.Errorf("RoomIDs = %v, want [random, general] (order preserved)", resp.RoomIDs)
	}
}

func TestHandleRoomJoined_TransportErrorReturns503(t *testing.T) {
	fake := &fakeUserGrainCaller{getJoinedErr: errors.New("cluster down")}
	g := gatewayWithFake(fake)
	rec := serveQuery(t, g, "GET /rooms/joined", "/rooms/joined", "alice")

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

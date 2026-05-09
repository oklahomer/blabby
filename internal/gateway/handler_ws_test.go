package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asynkron/protoactor-go/actor"
)

func TestHandleWS_NoClusterReturns503(t *testing.T) {
	g := NewGateway(&stubAuthenticator{}, nil, nil)
	srv := httptest.NewServer(g.RegisterRoutes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ws")
	if err != nil {
		t.Fatalf("GET /ws: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", resp.StatusCode)
	}

	var env ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != int(CodeServiceUnavailable) {
		t.Errorf("code: got %d, want %d", env.Error.Code, CodeServiceUnavailable)
	}
	if env.Error.Status != "SERVICE_UNAVAILABLE" {
		t.Errorf("status: got %q, want SERVICE_UNAVAILABLE", env.Error.Status)
	}
}

func TestHandleWS_PartialDependenciesReturns503(t *testing.T) {
	system := actor.NewActorSystem()
	// cluster is nil, actorRoot non-nil — must still be 503.
	g := NewGateway(&stubAuthenticator{}, nil, system.Root)
	srv := httptest.NewServer(g.RegisterRoutes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ws")
	if err != nil {
		t.Fatalf("GET /ws: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", resp.StatusCode)
	}
}

func TestHandleWS_NonGetReturnsMethodNotAllowed(t *testing.T) {
	g := NewGateway(&stubAuthenticator{}, nil, nil)
	srv := httptest.NewServer(g.RegisterRoutes())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/ws", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /ws: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want 405", resp.StatusCode)
	}
	if got := resp.Header.Get("Allow"); got != "GET" {
		t.Errorf("Allow header: got %q, want GET", got)
	}
}

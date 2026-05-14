package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

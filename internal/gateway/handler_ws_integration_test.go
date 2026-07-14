package gateway_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/actor/connection"
	"github.com/oklahomer/blabby/internal/gateway"
	"github.com/oklahomer/blabby/internal/grain/user"
	clustertest "github.com/oklahomer/blabby/internal/testutil/cluster"
)

func TestGateway_WebSocket_Integration(t *testing.T) {
	const userID = "1"
	const token = "integration-token-ws"

	c := clustertest.Start(t, user.NewKind(stubUserDirectory{}))
	g := gateway.NewGateway(gateway.Deps{
		Authenticator: &integrationAuth{userID: userID, token: token},
		Rooms:         newStubRoomDirectory(),
		Cluster:       c,
		ActorRoot:     c.ActorSystem.Root,
	})
	srv := httptest.NewServer(g.RegisterRoutes())
	t.Cleanup(srv.Close)

	wsURL := "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/ws"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"auth","token":"`+token+`"}`)); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	if got := readWebSocketFrame(t, client); got["type"] != "auth_ok" {
		t.Fatalf("expected auth_ok, got %v", got)
	}

	resp, err := userpb.GetUserGrainGrainClient(c, userID).
		ForwardMessage(&userpb.ForwardMessageRequest{
			Room:      &commonpb.RoomRef{RoomId: "4", PublicCode: "G000000004"},
			Sender:    &commonpb.UserRef{Id: "2", Name: "Bob Builder", PublicCode: "B000000002"},
			Text:      "hello-cluster",
			Timestamp: timestamppb.New(time.UnixMilli(1700000000000)),
			EventId:   "987654321",
		})
	if err != nil {
		t.Fatalf("ForwardMessage: %v", err)
	}
	if resp == nil {
		t.Fatal("ForwardMessage returned nil response")
	}

	got := readWebSocketFrame(t, client)
	if got["type"] != "message" || got["text"] != "hello-cluster" {
		t.Errorf("expected message frame with text=hello-cluster, got %v", got)
	}
	if got["event_id"] != "987654321" {
		t.Errorf("expected event_id=987654321, got %v", got["event_id"])
	}
	sender, ok := got["sender"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested sender object, got %v (%T)", got["sender"], got["sender"])
	}
	// The sender id on the wire is the client-facing U… code, never the internal id.
	if sender["id"] != "UB000000002" || sender["name"] != "Bob Builder" {
		t.Errorf("expected sender {id:UB000000002 name:Bob Builder}, got %v", sender)
	}
}

// TestGateway_WebSocket_HeartbeatKeepsPongingClientAlive proves the
// production wiring end to end: handleWS passes the gateway's cadence into
// the connection actor, pings flow through the real WS path, and a client
// that answers every ping outlives the pong timeout by several ping cycles.
// One pong only proves one watchdog reset, so the test answers eight —
// ~400ms at a 50ms cadence, double the 200ms timeout — and then confirms an
// application frame still arrives.
func TestGateway_WebSocket_HeartbeatKeepsPongingClientAlive(t *testing.T) {
	const userID = "1"
	const token = "integration-token-heartbeat-alive"

	c := clustertest.Start(t, user.NewKind(stubUserDirectory{}))
	g := gateway.NewGateway(gateway.Deps{
		Authenticator: &integrationAuth{userID: userID, token: token},
		Rooms:         newStubRoomDirectory(),
		Cluster:       c,
		ActorRoot:     c.ActorSystem.Root,
	})
	g.SetHeartbeatCadence(connection.MustHeartbeatCadence(50*time.Millisecond, 200*time.Millisecond))
	srv := httptest.NewServer(g.RegisterRoutes())
	t.Cleanup(srv.Close)

	client := dialAndAuth(t, srv.URL, token)

	for i := 0; i < 8; i++ {
		if got := readWebSocketFrame(t, client); got["type"] != "ping" {
			t.Fatalf("frame %d: expected ping, got %v", i, got)
		}
		if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"pong"}`)); err != nil {
			t.Fatalf("write pong %d: %v", i, err)
		}
	}

	// The session must still carry application traffic; interleaved pings
	// are answered while waiting so the watchdog stays satisfied.
	if _, err := userpb.GetUserGrainGrainClient(c, userID).
		ForwardMessage(&userpb.ForwardMessageRequest{
			Room:      &commonpb.RoomRef{RoomId: "4", PublicCode: "G000000004"},
			Sender:    &commonpb.UserRef{Id: "2", Name: "Bob Builder", PublicCode: "B000000002"},
			Text:      "still-alive",
			Timestamp: timestamppb.New(time.UnixMilli(1700000000000)),
			EventId:   "987654322",
		}); err != nil {
		t.Fatalf("ForwardMessage: %v", err)
	}
	for {
		got := readWebSocketFrame(t, client)
		if got["type"] == "ping" {
			if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"pong"}`)); err != nil {
				t.Fatalf("write pong while waiting: %v", err)
			}
			continue
		}
		if got["type"] != "message" || got["text"] != "still-alive" {
			t.Fatalf("expected message frame with text=still-alive, got %v", got)
		}
		return
	}
}

// TestGateway_WebSocket_HeartbeatClosesSilentClient is the closure half: a
// client that never answers pings is disconnected once the pong timeout
// elapses.
func TestGateway_WebSocket_HeartbeatClosesSilentClient(t *testing.T) {
	const userID = "1"
	const token = "integration-token-heartbeat-close"

	c := clustertest.Start(t, user.NewKind(stubUserDirectory{}))
	g := gateway.NewGateway(gateway.Deps{
		Authenticator: &integrationAuth{userID: userID, token: token},
		Rooms:         newStubRoomDirectory(),
		Cluster:       c,
		ActorRoot:     c.ActorSystem.Root,
	})
	g.SetHeartbeatCadence(connection.MustHeartbeatCadence(50*time.Millisecond, 200*time.Millisecond))
	srv := httptest.NewServer(g.RegisterRoutes())
	t.Cleanup(srv.Close)

	client := dialAndAuth(t, srv.URL, token)

	if got := readWebSocketFrame(t, client); got["type"] != "ping" {
		t.Fatalf("expected ping, got %v", got)
	}

	// Never pong: reads must fail with the server's close within the
	// watchdog window (200ms) plus generous scheduling slack.
	if err := client.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	for {
		_, data, err := client.ReadMessage()
		if err != nil {
			return // closed by the server — the expected outcome
		}
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if frame["type"] != "ping" {
			t.Fatalf("expected only pings before the close, got %v", frame)
		}
	}
}

// dialAndAuth dials srvURL's /ws route and completes the first-frame auth
// handshake, failing the test on any step.
func dialAndAuth(t *testing.T, srvURL, token string) *websocket.Conn {
	t.Helper()
	wsURL := "ws://" + strings.TrimPrefix(srvURL, "http://") + "/ws"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"auth","token":"`+token+`"}`)); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	if got := readWebSocketFrame(t, client); got["type"] != "auth_ok" {
		t.Fatalf("expected auth_ok, got %v", got)
	}
	return client
}

func readWebSocketFrame(t *testing.T, client *websocket.Conn) map[string]any {
	t.Helper()
	if err := client.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set WebSocket read deadline: %v", err)
	}
	messageType, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("read WebSocket frame: %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("WebSocket frame type = %d, want text", messageType)
	}

	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decode WebSocket frame: %v", err)
	}
	return frame
}

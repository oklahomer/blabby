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

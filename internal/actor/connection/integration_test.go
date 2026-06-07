package connection_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	connection "github.com/oklahomer/blabby/internal/actor/connection"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/id"
	clustertest "github.com/oklahomer/blabby/internal/testutil/cluster"
)

// integrationStubAuth is the test-local Authenticator. Defined in this
// _test package because connection_test.go's stubAuthenticator lives in
// the unexported in-package test file.
type integrationStubAuth struct {
	subject string
}

func (s *integrationStubAuth) Authenticate(_ context.Context, _ auth.AuthParams) (*auth.Result, error) {
	return nil, fmt.Errorf("not used in integration test")
}

func (s *integrationStubAuth) ValidateToken(_ context.Context, _ string) (*auth.Claims, error) {
	uid, err := id.NewUserID(s.subject)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", auth.ErrTokenInvalid, err)
	}
	return &auth.Claims{UserID: uid}, nil
}

func TestIntegration_AuthAndForwardThroughRealUserGrain(t *testing.T) {
	testCluster := clustertest.Start(t, user.NewKind(nil))
	authStub := &integrationStubAuth{subject: "alice-integration"}

	pidCh := make(chan *actor.PID, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		props := connection.NewProps(ws, authStub, testCluster)
		pidCh <- testCluster.ActorSystem.Root.Spawn(props)
	}))
	t.Cleanup(srv.Close)

	cli, _, err := websocket.DefaultDialer.Dial("ws://"+strings.TrimPrefix(srv.URL, "http://")+"/", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	pid := <-pidCh
	t.Cleanup(func() { _ = testCluster.ActorSystem.Root.PoisonFuture(pid).Wait() })

	// Auth — first frame.
	if err := cli.WriteMessage(websocket.TextMessage, []byte(`{"type":"auth","token":"x"}`)); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	if got := readFrame(t, cli); got["type"] != "auth_ok" {
		t.Fatalf("expected auth_ok, got %v", got)
	}

	// Drive a ForwardMessage through the real User grain. The grain's
	// fan-out routes the proto verbatim to every registered connection
	// PID — this connection is the only one, so we should see the JSON
	// envelope on the wire.
	resp, err := userpb.GetUserGrainGrainClient(testCluster, "alice-integration").
		ForwardMessage(&userpb.ForwardMessageRequest{
			RoomId:    "general",
			Sender:    &commonpb.UserRef{Id: "bob", Name: "Bob Builder"},
			Text:      "hello-cluster",
			Timestamp: timestamppb.New(time.UnixMilli(1700000000000)),
		})
	if err != nil {
		t.Fatalf("ForwardMessage: %v", err)
	}
	if resp == nil {
		t.Fatal("ForwardMessage returned nil response")
	}

	got := readFrame(t, cli)
	if got["type"] != "message" || got["text"] != "hello-cluster" {
		t.Errorf("expected message frame with text=hello-cluster, got %v", got)
	}
	sender, ok := got["sender"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested sender object, got %v (%T)", got["sender"], got["sender"])
	}
	if sender["id"] != "bob" || sender["name"] != "Bob Builder" {
		t.Errorf("expected sender {id:bob name:Bob Builder}, got %v", sender)
	}
}

func readFrame(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	mt, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if mt != websocket.TextMessage {
		t.Fatalf("expected text frame, got %d", mt)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

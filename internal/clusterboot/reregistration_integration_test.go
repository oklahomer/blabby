//go:build !race

package clusterboot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/gorilla/websocket"

	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/actor/connection"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/testutil/logcapture"
)

// TestMultiMemberGrainWatchReregistration proves the connection side of the
// bidirectional watch (ADR-006) end to end: a production UserConnection actor
// serving a real WebSocket, registered with a User grain hosted on the victim
// member, keeps receiving room messages after that member departs — without
// the client ever reconnecting.
//
// Isolation matters for what this test measures: only the connected user's
// grain lives on the victim. The room grain and the sending user are pinned
// to the survivor, so post-departure delivery can only fail or succeed on
// User-grain re-registration, not on room reactivation (that path is covered
// by TestMultiMemberDepartureAndReactivation).
func TestMultiMemberGrainWatchReregistration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("BLABBY_DATABASE_URL"))
	if dsn == "" {
		t.Skip("BLABBY_DATABASE_URL not set; skipping database integration test")
	}
	pool := openTestPool(t, dsn)
	seedDepartureFixtures(t, pool)

	members, rawSeeds := startTestMembers(t, 2, pool)
	gateway := startTestClient(t, rawSeeds, memberAddresses(members))
	client := gateway.cluster
	survivor := members[0].cluster

	userKind := testGrainKind{
		name: "UserGrain",
		activate: func(c *cluster.Cluster, identity string) error {
			_, err := userpb.GetUserGrainGrainClient(c, identity).
				GetJoinedRooms(&userpb.GetJoinedRoomsRequest{})
			return err
		},
	}
	roomKind := testGrainKind{
		name: "RoomGrain",
		activate: func(c *cluster.Cluster, identity string) error {
			_, err := roompb.GetRoomGrainGrainClient(c, identity).
				Leave(&roompb.LeaveRequest{UserId: "999999"})
			return err
		},
	}

	const victim = 1
	connectedUserID := findIdentityOn(t, client, members, victim, 1000, userKind)
	senderUserID := findIdentityOn(t, client, members, 0, 2000, userKind)
	roomID := findIdentityOn(t, client, members, 0, 3000, roomKind)

	// The server side of the WebSocket is a production UserConnection spawned
	// on the gateway node with the real cluster-backed grain caller, so the
	// watch, Terminated delivery, and re-register all run the shipped path.
	connectedUID, err := id.ParseUserID(connectedUserID)
	if err != nil {
		t.Fatalf("parse connected user id %q: %v", connectedUserID, err)
	}
	ws, connPID := dialUserConnection(t, client, &staticAuthenticator{claims: &auth.Claims{UserID: connectedUID}})
	writeWSFrame(t, ws, map[string]any{"type": "auth", "token": "integration-test-token"})
	readWSFrameOfType(t, ws, "auth_ok")

	requireCall(t, "join connected user to room", func() error {
		return joinRoom(client, connectedUserID, roomID)
	})
	requireCall(t, "join sender to room", func() error {
		return joinRoom(client, senderUserID, roomID)
	})

	// Baseline proves the full pre-departure path: sender → Room grain →
	// User grain (victim) → UserConnection → WebSocket.
	requireCall(t, "send baseline message", func() error {
		return sendMessage(client, senderUserID, roomID, "before-departure")
	})
	readWSMessageWithText(t, ws, "before-departure")

	logs := startTraceCapture(t)
	if err := shutdownCluster(members[victim].cluster); err != nil {
		t.Fatalf("depart member %d: %v", victim, err)
	}
	members[victim].cluster = nil
	waitForTopology(t, members[0].topologies, []string{survivor.ActorSystem.Address()})
	waitForTopology(t, gateway.topologies, []string{survivor.ActorSystem.Address()})

	// The death-watch fires and the connection re-registers on its own; no
	// message send is needed to trigger it. The test waits for the completed
	// repair before sending, because a message posted inside the death-watch
	// propagation window is lost by design (ADR-010) — that would test the
	// accepted gap, not the healing.
	waitForLogEvent(t, logs, "connection.reregister.succeeded")

	// One post-departure message. It must arrive on the same, never
	// reconnected WebSocket, via the re-registered connection.
	requireCall(t, "send after re-registration", func() error {
		return sendMessage(client, senderUserID, roomID, "after-departure")
	})
	readWSMessageWithText(t, ws, "after-departure")

	// Tear down in-body, survivor last, while the gateway endpoint is still
	// alive. The re-registered activation keeps the dead connection actor in
	// its watcher list (a watcher's death sends no Unwatch), so deactivating
	// it emits a Terminated toward the gateway; with the gateway already gone
	// — the LIFO t.Cleanup order — that notification has no endpoint and the
	// survivor's graceful shutdown stalls past the cleanup's 10s limit.
	_ = client.ActorSystem.Root.PoisonFuture(connPID).Wait()
	if err := shutdownCluster(members[0].cluster); err != nil {
		t.Fatalf("shut down survivor: %v", err)
	}
	members[0].cluster = nil
}

// staticAuthenticator satisfies auth.Authenticator with fixed claims, standing
// in for JWT validation; token contents are irrelevant to this test.
type staticAuthenticator struct {
	claims *auth.Claims
}

func (s *staticAuthenticator) Authenticate(context.Context, auth.AuthParams) (*auth.Result, error) {
	return nil, errors.New("not used in this test")
}

func (s *staticAuthenticator) ValidateToken(context.Context, string) (*auth.Claims, error) {
	return s.claims, nil
}

// dialUserConnection stands up a WebSocket endpoint whose server side is a
// production UserConnection actor spawned on the gateway node, and returns the
// dialed client side plus the actor's PID so the test can stop it in-body
// before the members go down. The cleanups it registers are the fallback for
// early exits; poisoning an already-stopped PID resolves immediately.
func dialUserConnection(t *testing.T, gateway *cluster.Cluster, authenticator auth.Authenticator) (*websocket.Conn, *actor.PID) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	pidCh := make(chan *actor.PID, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		pidCh <- gateway.ActorSystem.Root.Spawn(connection.NewProps(c, authenticator, gateway))
	}))
	t.Cleanup(srv.Close)

	ws, _, err := websocket.DefaultDialer.Dial("ws://"+strings.TrimPrefix(srv.URL, "http://"), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	pid := <-pidCh
	t.Cleanup(func() { _ = gateway.ActorSystem.Root.PoisonFuture(pid).Wait() })
	return ws, pid
}

func writeWSFrame(t *testing.T, ws *websocket.Conn, frame map[string]any) {
	t.Helper()
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// readWSFrameOfType reads frames until one of wantType arrives, skipping
// unrelated fan-outs (e.g. "joined" room events), and returns it.
func readWSFrameOfType(t *testing.T, ws *websocket.Conn, wantType string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(multiMemberTimeout)
	for {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket frame (want type %q): %v", wantType, err)
		}
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal frame %s: %v", data, err)
		}
		if frame["type"] == wantType {
			return frame
		}
	}
}

// readWSMessageWithText reads frames until the "message" frame carrying
// wantText arrives, skipping everything else.
func readWSMessageWithText(t *testing.T, ws *websocket.Conn, wantText string) {
	t.Helper()
	deadline := time.Now().Add(multiMemberTimeout)
	for {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket frame (want message %q): %v", wantText, err)
		}
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("unmarshal frame %s: %v", data, err)
		}
		if frame["type"] == "message" && frame["text"] == wantText {
			return
		}
	}
}

// waitForLogEvent polls the captured log stream for event; the emitting actor
// runs on its own mailbox goroutine, so the assertion has to wait.
func waitForLogEvent(t *testing.T, logs *logcapture.Buffer, event string) {
	t.Helper()
	requireEventually(t, "observe log event "+event, func() error {
		if !strings.Contains(logs.String(), event) {
			return errors.New("event not observed yet")
		}
		return nil
	})
}

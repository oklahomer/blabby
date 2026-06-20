package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/id"
)

// stubAuthenticator implements auth.Authenticator with caller-supplied
// behaviour. Authenticate is unused in this package's tests.
type stubAuthenticator struct {
	validateFn func(ctx context.Context, token string) (*auth.Claims, error)
}

func (s *stubAuthenticator) Authenticate(_ context.Context, _ auth.AuthParams) (*auth.Result, error) {
	return nil, errors.New("not implemented in stub")
}

func (s *stubAuthenticator) ValidateToken(ctx context.Context, token string) (*auth.Claims, error) {
	return s.validateFn(ctx, token)
}

// recordingGrainCaller captures every RegisterConnection invocation so
// tests can assert what the actor sent.
type recordingGrainCaller struct {
	mu    sync.Mutex
	calls []registerCall
	resp  *userpb.RegisterConnectionResponse
	err   error
}

type registerCall struct {
	userID string
	req    *userpb.RegisterConnectionRequest
}

func (r *recordingGrainCaller) RegisterConnection(userID string, req *userpb.RegisterConnectionRequest) (*userpb.RegisterConnectionResponse, error) {
	r.mu.Lock()
	r.calls = append(r.calls, registerCall{userID: userID, req: req})
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	if r.resp != nil {
		return r.resp, nil
	}
	return &userpb.RegisterConnectionResponse{}, nil
}

func (r *recordingGrainCaller) snapshot() []registerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]registerCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// session holds the parts of a single test session: the dialed client
// connection, the spawned actor PID, and the actor system used to spawn
// it. The cleanup function stops the actor and closes the server.
type session struct {
	client *websocket.Conn
	pid    *actor.PID
	system *actor.ActorSystem
}

func startSession(t *testing.T, authStub *stubAuthenticator, grainStub UserGrainCaller, opts ...Option) *session {
	t.Helper()

	system := actor.NewActorSystem()
	pidCh := make(chan *actor.PID, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		fullOpts := append([]Option{WithUserGrainCaller(grainStub)}, opts...)
		props := NewProps(c, authStub, nil, fullOpts...)
		pidCh <- system.Root.Spawn(props)
	}))
	t.Cleanup(srv.Close)

	cli, _, err := websocket.DefaultDialer.Dial("ws://"+strings.TrimPrefix(srv.URL, "http://")+"/", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	pid := <-pidCh
	t.Cleanup(func() {
		_ = system.Root.PoisonFuture(pid).Wait()
	})
	return &session{client: cli, pid: pid, system: system}
}

// readJSON reads a single text frame and decodes it into a map for
// flexible assertions on snake_case keys.
func readJSON(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if mt != websocket.TextMessage {
		t.Fatalf("expected text frame, got %d", mt)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return m
}

func writeAuthFrame(t *testing.T, c *websocket.Conn, token string) {
	t.Helper()
	frame := map[string]any{"type": "auth"}
	if token != "" {
		frame["token"] = token
	}
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func aliceClaims() *auth.Claims {
	uid, err := id.NewUserID(1)
	if err != nil {
		panic(err)
	}
	return &auth.Claims{UserID: uid}
}

// expectActorStops asserts that within d the spawned actor PID has been
// terminated. Uses Watch + *actor.Terminated via a probe actor.
func expectActorStops(t *testing.T, system *actor.ActorSystem, pid *actor.PID, d time.Duration) {
	t.Helper()
	doneCh := make(chan struct{}, 1)
	probe := actor.PropsFromFunc(func(ctx actor.Context) {
		switch ctx.Message().(type) {
		case *actor.Started:
			ctx.Watch(pid)
		case *actor.Terminated:
			select {
			case doneCh <- struct{}{}:
			default:
			}
		}
	})
	probePID := system.Root.Spawn(probe)
	t.Cleanup(func() { _ = system.Root.PoisonFuture(probePID).Wait() })

	select {
	case <-doneCh:
	case <-time.After(d):
		t.Fatalf("actor %s did not stop within %s", pid.String(), d)
	}
}

// --- Auth path scenarios -----------------------------------------------------

func TestAuth_HappyPath(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	grain := &recordingGrainCaller{}

	sess := startSession(t, authStub, grain)
	writeAuthFrame(t, sess.client, "valid")

	got := readJSON(t, sess.client)
	if got["type"] != "auth_ok" {
		t.Fatalf("expected auth_ok, got %v", got)
	}

	// RegisterConnection received the actor's PID
	calls := grain.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 RegisterConnection call, got %d", len(calls))
	}
	if calls[0].userID != "1" {
		t.Errorf("userID: got %q, want 1", calls[0].userID)
	}
	pid := calls[0].req.GetRequesterPid()
	if pid.GetAddress() == "" || pid.GetId() == "" {
		t.Errorf("requester_pid must be populated, got %+v", pid)
	}
}

func TestAuth_MissingTokenFieldYields1003(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	grain := &recordingGrainCaller{}
	sess := startSession(t, authStub, grain)
	writeAuthFrame(t, sess.client, "")

	got := readJSON(t, sess.client)
	if got["type"] != "auth_error" {
		t.Fatalf("expected auth_error, got %v", got)
	}
	if got["code"].(float64) != 1003 {
		t.Errorf("code: got %v, want 1003", got["code"])
	}
	if len(grain.snapshot()) != 0 {
		t.Errorf("RegisterConnection must not be called for missing token")
	}
	expectActorStops(t, sess.system, sess.pid, time.Second)
}

func TestAuth_InvalidTokenYields1001(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return nil, auth.ErrTokenInvalid
	}}
	grain := &recordingGrainCaller{}
	sess := startSession(t, authStub, grain)
	writeAuthFrame(t, sess.client, "junk")

	got := readJSON(t, sess.client)
	if got["code"].(float64) != 1001 {
		t.Errorf("code: got %v, want 1001", got["code"])
	}
	expectActorStops(t, sess.system, sess.pid, time.Second)
}

func TestAuth_ExpiredTokenYields1002(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return nil, auth.ErrTokenExpired
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{})
	writeAuthFrame(t, sess.client, "stale")

	got := readJSON(t, sess.client)
	if got["code"].(float64) != 1002 {
		t.Errorf("code: got %v, want 1002", got["code"])
	}
	expectActorStops(t, sess.system, sess.pid, time.Second)
}

func TestAuth_ContractViolationNilClaimsYields1001(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return nil, nil
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{})
	writeAuthFrame(t, sess.client, "weird")

	got := readJSON(t, sess.client)
	if got["code"].(float64) != 1001 {
		t.Errorf("code: got %v, want 1001 for nil-claims", got["code"])
	}
	expectActorStops(t, sess.system, sess.pid, time.Second)
}

func TestAuth_UnknownTypeBeforeAuthCanBeRetried(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	grain := &recordingGrainCaller{}
	sess := startSession(t, authStub, grain)
	if err := sess.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"message","text":"hi"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	writeAuthFrame(t, sess.client, "tok")

	got := readJSON(t, sess.client)
	if got["type"] != "auth_ok" {
		t.Fatalf("expected auth_ok after retry, got %v", got)
	}
	if got := len(grain.snapshot()); got != 1 {
		t.Fatalf("expected 1 RegisterConnection call after retry, got %d", got)
	}
}

func TestAuth_MalformedJSONBeforeAuthCanBeRetried(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	grain := &recordingGrainCaller{}
	sess := startSession(t, authStub, grain)
	if err := sess.client.WriteMessage(websocket.TextMessage, []byte(`not-json`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	writeAuthFrame(t, sess.client, "tok")

	got := readJSON(t, sess.client)
	if got["type"] != "auth_ok" {
		t.Fatalf("expected auth_ok after retry, got %v", got)
	}
	if got := len(grain.snapshot()); got != 1 {
		t.Fatalf("expected 1 RegisterConnection call after retry, got %d", got)
	}
}

func TestAuth_TimeoutWithoutFrameYields1003(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		t.Fatal("authenticator must not run on timeout path")
		return nil, nil
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{}, WithAuthTimeout(50*time.Millisecond))

	got := readJSON(t, sess.client)
	if got["type"] != "auth_error" || got["code"].(float64) != 1003 {
		t.Fatalf("expected timeout auth_error 1003, got %v", got)
	}
	expectActorStops(t, sess.system, sess.pid, time.Second)
}

func TestHeartbeat_PreAuthPingIsSent(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		t.Fatal("authenticator must not run before auth frame")
		return nil, nil
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{},
		WithAppHeartbeat(20*time.Millisecond, 500*time.Millisecond),
	)

	got := readJSON(t, sess.client)
	if got["type"] != "ping" {
		t.Fatalf("expected pre-auth ping, got %v", got)
	}
}

func TestAuth_RegisterTransportErrorYields5001AndStops(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	grain := &recordingGrainCaller{err: errors.New("boom")}
	sess := startSession(t, authStub, grain)
	writeAuthFrame(t, sess.client, "tok")

	got := readJSON(t, sess.client)
	if got["code"].(float64) != 5001 {
		t.Errorf("code: got %v, want 5001", got["code"])
	}
	expectActorStops(t, sess.system, sess.pid, time.Second)
}

func TestAuth_RegisterInlineErrorPropagates(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	grain := &recordingGrainCaller{
		resp: &userpb.RegisterConnectionResponse{
			Error: &commonpb.ErrorDetail{Code: 4001, Status: "INVALID_REQUEST", Message: "bad pid"},
		},
	}
	sess := startSession(t, authStub, grain)
	writeAuthFrame(t, sess.client, "tok")

	got := readJSON(t, sess.client)
	if got["code"].(float64) != 4001 {
		t.Errorf("code: got %v, want 4001", got["code"])
	}
	if got["status"] != "INVALID_REQUEST" {
		t.Errorf("status: got %v, want INVALID_REQUEST", got["status"])
	}
	expectActorStops(t, sess.system, sess.pid, time.Second)
}

func TestAuth_RegisterInlineErrorInvalidTaxonomyFailsClosed(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	grain := &recordingGrainCaller{
		resp: &userpb.RegisterConnectionResponse{
			Error: &commonpb.ErrorDetail{Code: 2001, Status: "ROOM_NOT_FOUND", Message: "bad pair"},
		},
	}
	sess := startSession(t, authStub, grain)
	writeAuthFrame(t, sess.client, "tok")

	got := readJSON(t, sess.client)
	if got["code"].(float64) != 5001 {
		t.Errorf("code: got %v, want 5001", got["code"])
	}
	if got["status"] != "INTERNAL_ERROR" {
		t.Errorf("status: got %v, want INTERNAL_ERROR", got["status"])
	}
	if got["message"] != "service unavailable" {
		t.Errorf("message: got %v, want service unavailable", got["message"])
	}
	expectActorStops(t, sess.system, sess.pid, time.Second)
}

// --- Server-push scenarios ---------------------------------------------------

func TestForwardMessage_AfterAuthSerialisesToWire(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{})
	writeAuthFrame(t, sess.client, "tok")
	_ = readJSON(t, sess.client) // auth_ok

	when := time.UnixMilli(1700000000000)
	sess.system.Root.Send(sess.pid, &userpb.ForwardMessageRequest{
		RoomId: "general", Sender: &commonpb.UserRef{Id: "bob", Name: "Bob Builder"},
		Text: "hello", Timestamp: timestamppb.New(when),
	})

	got := readJSON(t, sess.client)
	if got["type"] != "message" {
		t.Errorf("type: got %v, want message", got["type"])
	}
	if got["room_id"] != "general" || got["text"] != "hello" {
		t.Errorf("payload mismatch: %v", got)
	}
	if sender := userObject(t, got, "sender"); sender["id"] != "bob" || sender["name"] != "Bob Builder" {
		t.Errorf("sender: got %v, want {id:bob name:Bob Builder}", sender)
	}
	if got["timestamp"].(float64) != 1700000000000 {
		t.Errorf("timestamp: got %v, want 1700000000000", got["timestamp"])
	}
}

// userObject extracts the nested {"id","name"} object a message or
// room-event frame carries under key, failing the test if it is absent or
// not an object.
func userObject(t *testing.T, frame map[string]any, key string) map[string]any {
	t.Helper()
	obj, ok := frame[key].(map[string]any)
	if !ok {
		t.Fatalf("%s: got %v (%T), want nested object", key, frame[key], frame[key])
	}
	return obj
}

func TestRoomEvent_JoinedAndLeftSerialise(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{})
	writeAuthFrame(t, sess.client, "tok")
	_ = readJSON(t, sess.client)

	sess.system.Root.Send(sess.pid, &userpb.NotifyRoomEventRequest{
		RoomId: "general", User: &commonpb.UserRef{Id: "carol", Name: "Carol Danvers"},
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED,
	})
	joined := readJSON(t, sess.client)
	if joined["type"] != "joined" {
		t.Errorf("joined type: got %v, want joined", joined["type"])
	}
	if u := userObject(t, joined, "user"); u["id"] != "carol" || u["name"] != "Carol Danvers" {
		t.Errorf("joined user mismatch: %v", u)
	}

	sess.system.Root.Send(sess.pid, &userpb.NotifyRoomEventRequest{
		RoomId: "general", User: &commonpb.UserRef{Id: "carol", Name: "Carol Danvers"},
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT,
	})
	left := readJSON(t, sess.client)
	if left["type"] != "left" {
		t.Errorf("left type: got %v, want left", left["type"])
	}
	if u := userObject(t, left, "user"); u["id"] != "carol" || u["name"] != "Carol Danvers" {
		t.Errorf("left user mismatch: %v", u)
	}
}

func TestRoomEvent_UnspecifiedDoesNotCloseSession(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{})
	writeAuthFrame(t, sess.client, "tok")
	_ = readJSON(t, sess.client)

	sess.system.Root.Send(sess.pid, &userpb.NotifyRoomEventRequest{
		RoomId: "general", User: &commonpb.UserRef{Id: "carol", Name: "Carol Danvers"},
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_UNSPECIFIED,
	})

	// Push a known event afterwards. If the unknown event killed the session
	// we'd never see this frame.
	sess.system.Root.Send(sess.pid, &userpb.NotifyRoomEventRequest{
		RoomId: "general", User: &commonpb.UserRef{Id: "carol", Name: "Carol Danvers"},
		EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED,
	})
	if got := readJSON(t, sess.client); got["type"] != "joined" {
		t.Errorf("expected joined frame after unknown event drop, got %v", got)
	}
}

// --- Disconnect scenarios ----------------------------------------------------

func TestDisconnect_AfterAuthStopsActor(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{})
	writeAuthFrame(t, sess.client, "tok")
	_ = readJSON(t, sess.client)

	_ = sess.client.Close()
	expectActorStops(t, sess.system, sess.pid, time.Second)
}

func TestDisconnect_BeforeAuthSkipsRegister(t *testing.T) {
	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		t.Fatal("authenticator must not run when client closes before auth")
		return nil, nil
	}}
	grain := &recordingGrainCaller{}
	sess := startSession(t, authStub, grain)

	_ = sess.client.Close()
	expectActorStops(t, sess.system, sess.pid, time.Second)
	if got := len(grain.snapshot()); got != 0 {
		t.Errorf("RegisterConnection must not be called; got %d invocations", got)
	}
}

// --- Log hygiene (no token / no message-text in logs) ------------------------

// syncBuffer is a goroutine-safe sink for slog output. The Default slog
// logger may be written to concurrently from protoactor's internal
// goroutines while the test goroutine reads — bytes.Buffer is not safe
// under that pattern, so we wrap it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, s.buf.Len())
	copy(out, s.buf.Bytes())
	return out
}

func TestLogs_TokenIsNeverLogged(t *testing.T) {
	const secretToken = "ey-very-secret-jwt-payload"
	prev := slog.Default()
	buf := &syncBuffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	authStub := &stubAuthenticator{validateFn: func(_ context.Context, token string) (*auth.Claims, error) {
		if token != secretToken {
			t.Fatalf("authenticator received %q, want %q", token, secretToken)
		}
		return aliceClaims(), nil
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{})
	writeAuthFrame(t, sess.client, secretToken)
	_ = readJSON(t, sess.client)

	// Stop the actor before reading the buffer to ensure all writes have
	// landed (PoisonFuture in the session cleanup runs after the test, so
	// trigger an explicit drain here).
	_ = sess.system.Root.PoisonFuture(sess.pid).Wait()

	if got := buf.Snapshot(); bytes.Contains(got, []byte(secretToken)) {
		t.Errorf("token leaked into log output: %s", got)
	}
}

func TestLogs_MessageBodyIsNeverLogged(t *testing.T) {
	const secretBody = "DO-NOT-LOG-THIS-MESSAGE-BODY"
	prev := slog.Default()
	buf := &syncBuffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	authStub := &stubAuthenticator{validateFn: func(_ context.Context, _ string) (*auth.Claims, error) {
		return aliceClaims(), nil
	}}
	sess := startSession(t, authStub, &recordingGrainCaller{})
	writeAuthFrame(t, sess.client, "tok")
	_ = readJSON(t, sess.client)

	sess.system.Root.Send(sess.pid, &userpb.ForwardMessageRequest{
		RoomId: "general", Sender: &commonpb.UserRef{Id: "bob", Name: "Bob Builder"},
		Text:      secretBody,
		Timestamp: timestamppb.New(time.UnixMilli(1)),
	})
	_ = readJSON(t, sess.client)

	_ = sess.system.Root.PoisonFuture(sess.pid).Wait()

	got := buf.Snapshot()
	if bytes.Contains(got, []byte(secretBody)) {
		t.Errorf("message body leaked into log output: %s", got)
	}
	if !bytes.Contains(got, []byte("text_len")) {
		t.Errorf("expected text_len field in log output, got %s", got)
	}
}

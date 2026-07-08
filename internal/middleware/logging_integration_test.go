package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/id"
	clustertest "github.com/oklahomer/blabby/internal/testutil/cluster"
)

// activeRoomLoader reports every id as an active room so the Room grain can
// activate without a database in this trace test. It carries a valid public
// code because the Join flow returns the RoomRef to the User grain, whose
// boundary parse fails closed on a malformed code.
type activeRoomLoader struct{}

func (activeRoomLoader) LoadRoom(_ context.Context, roomID id.RoomID) (domain.RoomRef, error) {
	code, err := id.ParsePublicCode("G000000004")
	if err != nil {
		return domain.RoomRef{}, err
	}
	return domain.NewRoomRef(domain.RoomRefParams{
		ID:         roomID,
		PublicCode: code,
		Name:       "Room " + roomID.String(),
		Status:     domain.RoomStatusActive,
	})
}

// stubDirectory resolves every id to a UserRef with a valid public code, so the
// User grain's self carries the public identity the Room grain now requires on
// every command (a code-less self fails closed at the Room boundary).
type stubDirectory struct{}

func (stubDirectory) Resolve(_ context.Context, uid id.UserID) (domain.UserRef, error) {
	code, err := id.ParsePublicCode("A000000001")
	if err != nil {
		return domain.UserRef{}, err
	}
	return domain.NewUserRef(uid, code, "user-"+uid.String())
}

// syncBuffer is a goroutine-safe *bytes.Buffer wrapper. The slog handler
// is called from any actor mailbox goroutine, and the integration test
// asserts on the buffer from the test goroutine — bytes.Buffer is not
// safe for that combination.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestLoggingMiddleware_Integration_EndToEndTrace drives JoinRoom and
// SendMessage through the User grain client on a real cluster — the same
// route production commands take — then asserts that the captured JSON log
// stream reconstructs the full cross-grain trace (User command → Room →
// async fan-out back through the User grain) without leaking message text.
// Driving the User grain is safe because fan-out is an asynchronous
// notification off the Room grain's critical path (ADR-015); the self-echo
// deadlock this test once had to avoid no longer exists.
// UserConnection.connection.msg is covered by unit tests and the gateway
// package's own WebSocket integration tests.
func TestLoggingMiddleware_Integration_EndToEndTrace(t *testing.T) {
	// Cross-grain fan-out can activate several fresh identities, so use a longer
	// request timeout than the single-grain test default.
	c := clustertest.StartWithTimeout(t, 10*time.Second, room.NewKind(activeRoomLoader{}), user.NewKind(stubDirectory{}))
	captureBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(captureBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const alice = "1"
	const general = "4"

	uc := userpb.GetUserGrainGrainClient(c, alice)

	// 1) User.JoinRoom — the User grain commands Room.Join, whose fan-out
	//    dispatches NotifyRoomEvent back to alice's User grain (no registered
	//    connections, so target_count=0).
	joinResp, err := uc.JoinRoom(&userpb.JoinRoomRequest{RoomId: general})
	if err != nil {
		t.Fatalf("User.JoinRoom via cluster: %v", err)
	}
	if joinResp.GetError() != nil {
		t.Fatalf("User.JoinRoom: error %+v", joinResp.GetError())
	}

	// 2) User.SendMessage — the User grain commands Room.PostMessage, which
	//    assigns the server timestamp and fans out ForwardMessage back through
	//    alice's User grain. The text is a fresh UUID and must never appear in
	//    the log buffer.
	bodyMarker := uuid.NewString()
	sendResp, err := uc.SendMessage(&userpb.SendMessageRequest{
		RoomId: general,
		Text:   bodyMarker,
	})
	if err != nil {
		t.Fatalf("User.SendMessage via cluster: %v", err)
	}
	if sendResp.GetError() != nil {
		t.Fatalf("User.SendMessage: error %+v", sendResp.GetError())
	}

	// Wait for the User-grain fan-out lines to land in the buffer.
	settleFanOut(t, captureBuf)

	lines := parseJSONLines(t, captureBuf.String())

	// Trace assertions: every step in the cross-grain trace is present at
	// least once. The test's only client calls go to the User grain, so any
	// RoomGrain line proves the User→Room command leg, and the UserGrain
	// grain.fanout lines prove the asynchronous notify/forward legs arrived
	// back. Lifecycle events (grain.activated) are covered by the package's
	// unit tests.
	expect := []struct {
		event     string
		grainType string
		grainID   string // empty = match any
	}{
		// JoinRoom trail: User command envelope → Room → fan-out → User notify.
		{event: "grain.msg", grainType: "UserGrain", grainID: alice},   // JoinRoom envelope
		{event: "grain.msg", grainType: "RoomGrain", grainID: general}, // Join envelope
		{event: "room.member.joined", grainType: "RoomGrain", grainID: general},
		{event: "grain.fanout", grainType: "RoomGrain", grainID: general}, // Join.fanout
		{event: "grain.fanout", grainType: "UserGrain", grainID: alice},   // NotifyRoomEvent.fanout

		// SendMessage trail: User command envelope → Room → fan-out → User forward.
		{event: "grain.msg", grainType: "RoomGrain", grainID: general}, // PostMessage envelope
		{event: "room.message.posted", grainType: "RoomGrain", grainID: general},
		{event: "grain.fanout", grainType: "RoomGrain", grainID: general}, // PostMessage.fanout
		{event: "grain.fanout", grainType: "UserGrain", grainID: alice},   // ForwardMessage.fanout
	}
	for _, want := range expect {
		if !containsLine(lines, want.event, want.grainType, want.grainID) {
			t.Errorf("missing trace line: event=%q grain_type=%q grain_id=%q", want.event, want.grainType, want.grainID)
		}
	}

	// The message body must never appear in any captured log line.
	full := captureBuf.String()
	if strings.Contains(full, bodyMarker) {
		t.Errorf("captured log stream leaked message body %q", bodyMarker)
	}

	// Defense-in-depth against future regressions: well-known forbidden
	// substrings that flag accidental dumps of full request payloads.
	for _, forbidden := range []string{`"token":`, `"password":`, `"secret":`, `"body":`} {
		if strings.Contains(full, forbidden) {
			t.Errorf("captured log stream contained forbidden substring %q", forbidden)
		}
	}
}

// settleFanOut waits up to a deadline for the User-grain ForwardMessage
// fan-out line to appear in the buffer, indicating the Room→User
// round-trip from PostMessage has completed. Best-effort; if the line
// never appears the subsequent expect[] check surfaces the gap.
func settleFanOut(t *testing.T, buf *syncBuffer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), `"msg_type":"ForwardMessage.fanout"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func parseJSONLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Logf("skipping non-JSON line: %s", line)
			continue
		}
		out = append(out, entry)
	}
	return out
}

func containsLine(lines []map[string]any, event, grainType, grainID string) bool {
	for _, line := range lines {
		if line["msg"] != event {
			continue
		}
		if grainType != "" && line["grain_type"] != grainType {
			continue
		}
		if grainID == "" {
			return true
		}
		if line["grain_id"] == grainID {
			return true
		}
	}
	return false
}

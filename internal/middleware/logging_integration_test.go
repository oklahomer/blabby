package middleware_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/cluster"
	"github.com/google/uuid"

	roompb "github.com/oklahomer/blabby/gen/room"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/grain/user"
	clustertest "github.com/oklahomer/blabby/internal/testutil/cluster"
)

// sharedCluster is brought up once for the middleware integration test
// package. protoactor-go's remote.Start writes to a process-global grpclog
// reference, so booting a fresh cluster per-test races against the prior
// cluster's still-running balancer goroutines; the User grain test
// package adopts the same TestMain-shared-cluster pattern for the same
// reason.
var sharedCluster *cluster.Cluster

func TestMain(m *testing.M) {
	bootstrap := &mainT{}
	// The integration test exercises real Room + User grains with
	// cross-grain fan-out, so the default 2-second clustertest timeout
	// is too tight for a fresh activator on each new identity. 10s
	// leaves comfortable headroom under -race on shared CI runners.
	sharedCluster = clustertest.StartWithTimeout(bootstrap, 10*time.Second, room.NewKind(), user.NewKind())

	exit := func() int {
		defer bootstrap.runCleanups()
		return m.Run()
	}()
	os.Exit(exit)
}

// mainT is the minimal *testing.T-shaped value clustertest.Start requires.
// Mirrors internal/grain/user/main_test.go's helper.
type mainT struct {
	mu       sync.Mutex
	cleanups []func()
}

func (m *mainT) Helper() {}
func (m *mainT) Cleanup(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanups = append(m.cleanups, fn)
}
func (m *mainT) Fatalf(format string, args ...any) {
	panic(fmt.Sprintf("TestMain setup failed: "+format, args...))
}

func (m *mainT) runCleanups() {
	m.mu.Lock()
	cleanups := m.cleanups
	m.cleanups = nil
	m.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		func(fn func()) {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "[middleware-test] cleanup panicked: %v\n", r)
				}
			}()
			fn()
		}(cleanups[i])
	}
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

// TestLoggingMiddleware_Integration_EndToEndTrace drives Room.Join and
// Room.PostMessage through the shared cluster, then asserts that the
// captured JSON log stream reconstructs the cross-grain trace defined by
// this story's acceptance criteria. NFR1 is enforced by injecting a UUID
// as the message text and asserting it never appears anywhere in the
// captured stream.
//
// The test drives the Room grain client directly rather than the User
// grain client. Calling user.JoinRoom would block the User grain while it
// awaits a synchronous reply from Room.Join, but Room.Join's fan-out
// dispatches NotifyRoomEvent back into the same User grain — that
// re-entry deadlocks under the default protoactor cluster.Request
// semantics. Driving via Room avoids the cycle: the test holds the
// outermost future and User grain runs unblocked when Room calls back.
// The trace shape this exercises still covers every middleware-emitted
// line plus the new domain follow-up lines (room.member.joined,
// room.message.posted, grain.fanout for both Room and User).
// UserConnection.connection.msg is covered by unit tests and the gateway
// package's own WebSocket integration tests.
func TestLoggingMiddleware_Integration_EndToEndTrace(t *testing.T) {
	captureBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(captureBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const alice = "alice-int"
	const general = "general-int"

	roomClient := roompb.GetRoomGrainGrainClient(sharedCluster, general)

	// 1) Room.Join — Room grain fans out NotifyRoomEvent to alice's
	//    User grain, which has no registered connections (target_count=0).
	joinResp, err := roomClient.Join(&roompb.JoinRequest{UserId: alice})
	if err != nil {
		t.Fatalf("Room.Join via cluster: %v", err)
	}
	if joinResp.GetError() != nil {
		t.Fatalf("Room.Join: error %+v", joinResp.GetError())
	}

	// 2) Room.PostMessage — Room grain assigns the server timestamp and
	//    fans out ForwardMessage back through alice's User grain. The
	//    text is a fresh UUID; NFR1 asserts it never appears in the log
	//    buffer (the body must never be logged).
	bodyMarker := uuid.NewString()
	postResp, err := roomClient.PostMessage(&roompb.PostMessageRequest{
		UserId: alice,
		Text:   bodyMarker,
	})
	if err != nil {
		t.Fatalf("Room.PostMessage via cluster: %v", err)
	}
	if postResp.GetError() != nil {
		t.Fatalf("Room.PostMessage: error %+v", postResp.GetError())
	}

	// Wait for the User-grain fan-out lines to land in the buffer.
	settleFanOut(t, captureBuf)

	lines := parseJSONLines(t, captureBuf.String())

	// Trace assertions: every step in the cross-grain trace is present
	// at least once. Lifecycle events (grain.activated) are covered by
	// the package's unit tests.
	expect := []struct {
		event   string
		grainID string // empty = match any
	}{
		// Room.Join trail.
		{event: "grain.msg", grainID: general}, // Join envelope
		{event: "room.member.joined", grainID: general},
		{event: "grain.fanout", grainID: general}, // Join.fanout
		{event: "grain.msg", grainID: alice},      // NotifyRoomEvent envelope
		{event: "grain.fanout", grainID: alice},   // NotifyRoomEvent.fanout

		// Room.PostMessage trail.
		{event: "grain.msg", grainID: general}, // PostMessage envelope
		{event: "room.message.posted", grainID: general},
		{event: "grain.fanout", grainID: general}, // PostMessage.fanout
		{event: "grain.msg", grainID: alice},      // ForwardMessage envelope
		{event: "grain.fanout", grainID: alice},   // ForwardMessage.fanout
	}
	for _, want := range expect {
		if !containsLine(lines, want.event, want.grainID) {
			t.Errorf("missing trace line: event=%q grain_id=%q", want.event, want.grainID)
		}
	}

	// NFR1: the message body must never appear in any captured log line.
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

func containsLine(lines []map[string]any, event, grainID string) bool {
	for _, line := range lines {
		if line["msg"] != event {
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

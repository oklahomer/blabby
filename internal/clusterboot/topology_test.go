package clusterboot

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"

	"github.com/oklahomer/blabby/internal/testutil/logcapture"
)

// allowedMembershipKeys is the complete set of fields a cluster-membership log
// line may carry. Asserting against it proves the line contains only
// non-sensitive membership data, never request payloads or credentials.
var allowedMembershipKeys = map[string]bool{
	"time":         true,
	"level":        true,
	"msg":          true,
	"node_address": true,
	"kinds":        true,
}

func TestLogTopologyChange(t *testing.T) {
	lines := captureJSONLogs(t, func() {
		logTopologyChange(&cluster.ClusterTopology{
			Joined: []*cluster.Member{
				{Host: "10.0.0.2", Port: 8091, Kinds: []string{"UserGrain", "RoomGrain"}},
			},
			Left: []*cluster.Member{
				{Host: "10.0.0.3", Port: 8092, Kinds: []string{"UserGrain"}},
			},
		})
	})

	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2: %v", len(lines), lines)
	}

	joined := lines[0]
	if joined["msg"] != "server.cluster.member_joined" {
		t.Errorf("line 0 msg = %v, want server.cluster.member_joined", joined["msg"])
	}
	if joined["node_address"] != "10.0.0.2:8091" {
		t.Errorf("joined node_address = %v, want 10.0.0.2:8091", joined["node_address"])
	}
	assertKinds(t, joined, []string{"UserGrain", "RoomGrain"})

	left := lines[1]
	if left["msg"] != "server.cluster.member_left" {
		t.Errorf("line 1 msg = %v, want server.cluster.member_left", left["msg"])
	}
	if left["node_address"] != "10.0.0.3:8092" {
		t.Errorf("left node_address = %v, want 10.0.0.3:8092", left["node_address"])
	}
	assertKinds(t, left, []string{"UserGrain"})

	for _, ln := range lines {
		for k := range ln {
			if !allowedMembershipKeys[k] {
				t.Errorf("unexpected log field %q (possible payload leak) in %v", k, ln)
			}
		}
	}
}

// TestTopologyLogHandlerThroughEventStream drives the handler exactly as the
// server wires it: subscribed to a real actor-system EventStream. It also
// publishes non-topology events to prove the type switch ignores everything
// that is not a *cluster.ClusterTopology.
func TestTopologyLogHandlerThroughEventStream(t *testing.T) {
	system := actor.NewActorSystem()
	sub := system.EventStream.Subscribe(topologyLogHandler)
	defer system.EventStream.Unsubscribe(sub)

	lines := captureJSONLogs(t, func() {
		// Neither of these is a topology event; both must be ignored.
		system.EventStream.Publish("not a topology event")
		system.EventStream.Publish(42)

		// Only this drives a membership log line.
		system.EventStream.Publish(&cluster.ClusterTopology{
			Joined: []*cluster.Member{
				{Host: "10.0.0.2", Port: 8091, Kinds: []string{"UserGrain"}},
			},
		})
	})

	// Isolate our handler's output from any incidental framework logging.
	var membership []map[string]any
	for _, ln := range lines {
		if msg, _ := ln["msg"].(string); strings.HasPrefix(msg, "server.cluster.") {
			membership = append(membership, ln)
		}
	}

	if len(membership) != 1 {
		t.Fatalf("got %d server.cluster.* lines, want exactly 1 (non-topology events must be ignored): %v", len(membership), membership)
	}
	if membership[0]["msg"] != "server.cluster.member_joined" {
		t.Errorf("msg = %v, want server.cluster.member_joined", membership[0]["msg"])
	}
	if membership[0]["node_address"] != "10.0.0.2:8091" {
		t.Errorf("node_address = %v, want 10.0.0.2:8091", membership[0]["node_address"])
	}
}

// captureJSONLogs installs a JSON slog handler over a buffer for the duration
// of fn, then parses the newline-delimited output into one map per line. The
// previous default logger is restored on cleanup.
func captureJSONLogs(t *testing.T, fn func()) []map[string]any {
	t.Helper()

	buf := logcapture.JSON(t, slog.LevelInfo)

	fn()

	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			t.Fatalf("log line is not valid JSON: %v\nline=%s", err, raw)
		}
		lines = append(lines, entry)
	}
	return lines
}

// assertKinds checks that a captured log line's "kinds" field is the JSON array
// equivalent of want.
func assertKinds(t *testing.T, line map[string]any, want []string) {
	t.Helper()

	raw, ok := line["kinds"].([]any)
	if !ok {
		t.Fatalf("kinds field = %v (%T), want JSON array", line["kinds"], line["kinds"])
	}
	got := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("kinds element = %v (%T), want string", v, v)
		}
		got = append(got, s)
	}
	if !slices.Equal(got, want) {
		t.Errorf("kinds = %v, want %v", got, want)
	}
}

package room

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/id"
)

// These tests drive the real fan-out worker under its real supervisor through
// a plain actor system. WithSupervisor is installed on the parent props exactly
// as NewKind installs it on the Room grain props.

type panickyNotifier struct {
	mu     sync.Mutex
	calls  int
	onCall func(call int)
	served chan int
}

func (n *panickyNotifier) NotifyRoomEvent(_ id.UserID, _ *userpb.NotifyRoomEventRequest) error {
	return n.invoke()
}

func (n *panickyNotifier) ForwardMessage(_ id.UserID, _ *userpb.ForwardMessageRequest) error {
	return n.invoke()
}

func (n *panickyNotifier) invoke() error {
	n.mu.Lock()
	call := n.calls
	n.calls++
	n.mu.Unlock()
	if n.onCall != nil {
		n.onCall(call)
	}
	if n.served != nil {
		n.served <- call
	}
	return nil
}

type fanoutHarness struct {
	notifier userNotifier
	ready    chan *actor.PID
	spawned  int32
}

func (h *fanoutHarness) Receive(ctx actor.Context) {
	if _, ok := ctx.Message().(*actor.Started); !ok {
		return
	}
	worker := ctx.Spawn(actor.PropsFromProducer(func() actor.Actor {
		atomic.AddInt32(&h.spawned, 1)
		return &fanoutWorker{notifier: h.notifier}
	}))
	h.ready <- worker
}

func startFanoutHarness(t *testing.T, n userNotifier, logger *slog.Logger) (*actor.ActorSystem, *fanoutHarness, *actor.PID) {
	t.Helper()
	system := actor.NewActorSystem()
	h := &fanoutHarness{notifier: n, ready: make(chan *actor.PID, 1)}
	props := actor.PropsFromProducer(
		func() actor.Actor { return h },
		actor.WithSupervisor(newFanoutSupervisor(logger)),
	)
	system.Root.Spawn(props)
	t.Cleanup(system.Shutdown)
	return system, h, <-h.ready
}

func notifyJob(t *testing.T) *fanoutNotify {
	t.Helper()
	uid, err := id.NewUserID("alice")
	if err != nil {
		t.Fatalf("user id: %v", err)
	}
	return &fanoutNotify{
		recipients: []id.UserID{uid},
		payload:    &userpb.NotifyRoomEventRequest{RoomId: "general"},
		msgType:    "Join.fanout",
		grainKind:  roomGrainKind,
		grainID:    "general",
	}
}

func TestFanoutWorkerRestartDropsFailedJobAndContinuesMailbox(t *testing.T) {
	served := make(chan int, 1)
	n := &panickyNotifier{served: served, onCall: func(call int) {
		if call == 0 {
			panic("unexpected notifier panic")
		}
	}}
	logs := &syncBuffer{}
	system, h, worker := startFanoutHarness(t, n, slog.New(slog.NewJSONHandler(logs, nil)))

	// Both jobs target the same PID. The first is dropped when it panics; the
	// worker actor instance restarts and then processes the second queued job.
	system.Root.Send(worker, notifyJob(t))
	system.Root.Send(worker, notifyJob(t))

	select {
	case call := <-served:
		if call != 1 {
			t.Fatalf("served call = %d, want second job (call 1)", call)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not process the next queued job after restart")
	}

	if got := atomic.LoadInt32(&h.spawned); got != 2 {
		t.Errorf("worker instances = %d, want 2 (initial instance plus one restart)", got)
	}

	line := requireSupervisionLine(t, logs)
	assertLine(t, line, "directive", actor.RestartDirective.String())
	assertLine(t, line, "grain_type", roomGrainKind)
	assertLine(t, line, "grain_id", "general")
	assertLine(t, line, "error", "unexpected notifier panic")
	if msgType, _ := line["msg_type"].(string); !strings.Contains(msgType, "fanoutNotify") {
		t.Errorf("msg_type = %q, want fanoutNotify type", msgType)
	}
}

// syncBuffer is a goroutine-safe sink for the JSON slog handler the supervisor
// writes to from the actor goroutine while the test reads from its own.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func requireSupervisionLine(t *testing.T, logs *syncBuffer) map[string]any {
	t.Helper()
	for _, raw := range bytes.Split([]byte(logs.String()), []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			continue
		}
		if line["msg"] == eventRoomFanoutSupervision {
			return line
		}
	}
	t.Fatalf("no %s log line emitted; got: %s", eventRoomFanoutSupervision, logs.String())
	return nil
}

func assertLine(t *testing.T, line map[string]any, key, want string) {
	t.Helper()
	if got, _ := line[key].(string); got != want {
		t.Errorf("log %q = %q, want %q", key, got, want)
	}
}

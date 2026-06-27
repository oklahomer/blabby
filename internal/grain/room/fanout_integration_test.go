package room_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"

	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/id"
	clustertest "github.com/oklahomer/blabby/internal/testutil/cluster"
)

// stubDirectory is a user.Directory returning a fixed display name for any id,
// so the test can prove the seeded name propagates end-to-end through both
// real grains to the connection.
type stubDirectory struct{ name string }

func (d stubDirectory) Resolve(_ context.Context, uid id.UserID) (id.UserRef, error) {
	return id.NewUserRef(uid, d.name)
}

// fanoutProbe is a regular actor that records the fan-out messages a User
// grain delivers to a registered connection PID. It stands in for a
// UserConnection actor so the test can observe end-to-end fan-out delivery.
type fanoutProbe struct {
	mu       sync.Mutex
	notifies []*userpb.NotifyRoomEventRequest
	forwards []*userpb.ForwardMessageRequest
}

func (p *fanoutProbe) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *userpb.NotifyRoomEventRequest:
		p.mu.Lock()
		p.notifies = append(p.notifies, msg)
		p.mu.Unlock()
	case *userpb.ForwardMessageRequest:
		p.mu.Lock()
		p.forwards = append(p.forwards, msg)
		p.mu.Unlock()
	}
}

func (p *fanoutProbe) firstNotify() (*userpb.NotifyRoomEventRequest, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.notifies) == 0 {
		return nil, false
	}
	return p.notifies[0], true
}

func (p *fanoutProbe) firstForward() (*userpb.ForwardMessageRequest, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.forwards) == 0 {
		return nil, false
	}
	return p.forwards[0], true
}

// TestFanout_SelfEcho_NoDeadlock_AndDelivers brings up a real single-member
// cluster with both grain kinds and verifies the ADR-015 fan-out fix:
//
//   - A sole member's JoinRoom and SendMessage return success rather than
//     timing out. Before the fix the Room grain fanned the event back into the
//     same User grain that was still blocked awaiting the command, deadlocking
//     until the request timeout.
//   - The events are still delivered: a probe registered as the user's
//     connection receives the JOINED event and the forwarded message.
func TestFanout_SelfEcho_NoDeadlock_AndDelivers(t *testing.T) {
	const displayName = "Alice Example"
	const userID = "1"
	const roomID = "4"
	c := clustertest.Start(t, user.NewKind(stubDirectory{name: displayName}), room.NewKind(seededLoader(activeRoomRef(t, roomID))))

	probe := &fanoutProbe{}
	probePID := c.ActorSystem.Root.Spawn(actor.PropsFromProducer(func() actor.Actor { return probe }))

	uc := userpb.GetUserGrainGrainClient(c, userID)

	// Register the probe as the user's connection so fan-out has a target.
	if _, err := uc.RegisterConnection(&userpb.RegisterConnectionRequest{
		RequesterPid: &userpb.PID{Address: probePID.Address, Id: probePID.Id},
	}); err != nil {
		t.Fatalf("RegisterConnection: %v", err)
	}

	// Join — the deadlock case. Must return success well within the cluster
	// request timeout instead of timing out.
	joinResp, err := uc.JoinRoom(&userpb.JoinRoomRequest{RoomId: roomID})
	if err != nil {
		t.Fatalf("JoinRoom returned a transport error (deadlock regression): %v", err)
	}
	if ed := joinResp.GetError(); ed != nil {
		t.Fatalf("JoinRoom business error: code=%d status=%q", ed.GetCode(), ed.GetStatus())
	}

	// Send — the message-echo deadlock case.
	sendResp, err := uc.SendMessage(&userpb.SendMessageRequest{RoomId: roomID, Text: "hello"})
	if err != nil {
		t.Fatalf("SendMessage returned a transport error (deadlock regression): %v", err)
	}
	if ed := sendResp.GetError(); ed != nil {
		t.Fatalf("SendMessage business error: code=%d status=%q", ed.GetCode(), ed.GetStatus())
	}
	if sendResp.GetTimestamp() == nil {
		t.Error("SendMessage response missing the server timestamp")
	}

	// Fan-out is asynchronous, so the echoes arrive shortly after the command
	// responses. Poll until both land (or fail on timeout).
	waitFor(t, "JOINED event delivered to connection", func() bool {
		_, ok := probe.firstNotify()
		return ok
	})
	waitFor(t, "forwarded message delivered to connection", func() bool {
		_, ok := probe.firstForward()
		return ok
	})

	notify, _ := probe.firstNotify()
	if notify.GetRoom().GetRoomId() != roomID || notify.GetEventType() != userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED {
		t.Errorf("notify: got room=%q type=%v, want room=%q JOINED",
			notify.GetRoom().GetRoomId(), notify.GetEventType(), roomID)
	}
	if got := notify.GetUser().GetName(); got != displayName {
		t.Errorf("JOINED user name: got %q, want %q (the directory-seeded name must reach the connection)", got, displayName)
	}
	forward, _ := probe.firstForward()
	if forward.GetRoom().GetRoomId() != roomID || forward.GetText() != "hello" {
		t.Errorf("forward: got room=%q text=%q, want room=%q text=%q",
			forward.GetRoom().GetRoomId(), forward.GetText(), roomID, "hello")
	}
	if got := forward.GetSender().GetName(); got != displayName {
		t.Errorf("forwarded sender name: got %q, want %q (the directory-seeded name must reach the connection)", got, displayName)
	}
}

// waitFor polls cond up to a generous deadline, failing the test if it never
// becomes true. Used for the asynchronous fan-out delivery.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

package user_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
)

// receivingActor is a real actor used as a stand-in for the UserConnection
// actor. It records every ForwardMessageRequest it receives through its
// mailbox so tests can assert end-to-end fan-out delivery.
//
// registerResp / registerErr capture the outcome of the RegisterConnection
// cluster call so registerSelfWithPID can fail loudly on a bad response
// instead of silently masking a registration failure.
type receivingActor struct {
	mu           sync.Mutex
	received     []*userpb.ForwardMessageRequest
	registered   chan struct{} // closed once RegisterConnection returns
	registerResp *userpb.RegisterConnectionResponse
	registerErr  error
}

// registerSelfCmd asks a receivingActor to register itself with the User
// grain. The actor invokes the cluster client from its OWN goroutine and
// includes its own PID (ctx.Self()) in the request body — proving the
// PID-in-payload wire-format design actually delivers messages.
type registerSelfCmd struct {
	cluster *cluster.Cluster
	userID  string
}

func (r *receivingActor) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		// nothing
	case registerSelfCmd:
		self := ctx.Self()
		client := userpb.GetUserGrainGrainClient(msg.cluster, msg.userID)
		resp, err := client.RegisterConnection(&userpb.RegisterConnectionRequest{
			RequesterPid: &userpb.PID{
				Address: self.GetAddress(),
				Id:      self.GetId(),
			},
		})
		r.registerResp = resp
		r.registerErr = err
		close(r.registered)
	case *userpb.ForwardMessageRequest:
		r.mu.Lock()
		r.received = append(r.received, msg)
		r.mu.Unlock()
	}
}

func (r *receivingActor) Received() []*userpb.ForwardMessageRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*userpb.ForwardMessageRequest, len(r.received))
	copy(out, r.received)
	return out
}

// registerSelfWithPID sends the register command to an actor's PID, waits
// for the round-trip to complete, and fails the test if the registration
// returned a transport error or success=false. Inspecting the response is
// necessary so a future regression in the PID-validation path cannot turn
// a registration failure into a silent test pass (the rest of the test
// would then check delivery against a never-registered receiver).
func registerSelfWithPID(t *testing.T, c *cluster.Cluster, pid *actor.PID, r *receivingActor, userID string) {
	t.Helper()
	c.ActorSystem.Root.Send(pid, registerSelfCmd{cluster: c, userID: userID})
	select {
	case <-r.registered:
	case <-time.After(3 * time.Second):
		t.Fatalf("RegisterConnection for %s never returned", pid.GetId())
	}
	if r.registerErr != nil {
		t.Fatalf("RegisterConnection transport error for %s: %v", pid.GetId(), r.registerErr)
	}
	if ed := r.registerResp.GetError(); ed != nil {
		t.Fatalf("RegisterConnection for %s failed: code=%d status=%q msg=%q",
			pid.GetId(), ed.GetCode(), ed.GetStatus(), ed.GetMessage())
	}
}

// spawnReceiver spawns a receivingActor and returns both the actor and its
// PID so the caller can drive register/teardown. PoisonFuture is wrapped
// with a timeout so a stuck mailbox cannot hang the entire `go test` run;
// this is a defensive guard rather than expected behavior.
func spawnReceiver(t *testing.T, c *cluster.Cluster) (*receivingActor, *actor.PID) {
	t.Helper()
	r := &receivingActor{registered: make(chan struct{})}
	props := actor.PropsFromProducer(func() actor.Actor { return r })
	pid := c.ActorSystem.Root.Spawn(props)
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() {
			_ = c.ActorSystem.Root.PoisonFuture(pid).Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Logf("warning: PoisonFuture timed out for receiver %s", pid.GetId())
		}
	})
	return r, pid
}

// countDeadLettersTo counts dead letters addressed to pid via the actor
// system's EventStream, unsubscribing when the test ends. A fan-out attempt
// at a stopped connection dead-letters, so the returned load func lets the
// eviction tests assert the grain actually stopped targeting a PID — not
// merely that a surviving connection still receives.
//
// EventStream.Publish runs subscribers synchronously on the sending
// goroutine and the grain responds to an RPC only after its handler's sends
// completed, so a count read after a ForwardMessage call returns is final
// for that round.
func countDeadLettersTo(t *testing.T, c *cluster.Cluster, pid *actor.PID) func() int64 {
	t.Helper()
	var n atomic.Int64
	sub := c.ActorSystem.EventStream.Subscribe(func(evt any) {
		if dl, ok := evt.(*actor.DeadLetterEvent); ok && dl.PID != nil && dl.PID.Equal(pid) {
			n.Add(1)
		}
	})
	t.Cleanup(func() { c.ActorSystem.EventStream.Unsubscribe(sub) })
	return n.Load
}

// forwardUntilEvicted keeps issuing ForwardMessage rounds until one full
// round produces no new dead letter at deadPID (the eviction has taken
// effect), failing the test if that never happens within the deadline. Each
// round's delivery is confirmed at the live receiver so a slow round is not
// mistaken for eviction.
func forwardUntilEvicted(t *testing.T, c *cluster.Cluster, userID string, req *userpb.ForwardMessageRequest, deadPID *actor.PID, rLive *receivingActor, alreadyReceived int) {
	t.Helper()
	deadLetters := countDeadLettersTo(t, c, deadPID)
	uc := userpb.GetUserGrainGrainClient(c, userID)
	deadline := time.Now().Add(2 * time.Second)
	for round := 1; time.Now().Before(deadline); round++ {
		before := deadLetters()
		if _, err := uc.ForwardMessage(req); err != nil {
			t.Fatalf("ForwardMessage via cluster: %v", err)
		}
		after := deadLetters()
		waitForReceived(t, rLive, alreadyReceived+round)
		if after == before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("grain kept fanning out to the dead PID %s until the deadline (no eviction)", deadPID.GetId())
}

// waitForReceived polls until the actor has received at least `want`
// messages, or fails the test on timeout. Failing here turns "delivery
// timed out" into a self-explanatory test failure rather than leaving the
// caller's downstream `len() != want` check to misattribute it as a
// delivery-count bug.
func waitForReceived(t *testing.T, r *receivingActor, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(r.Received()) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d messages, got %d", want, len(r.Received()))
}

// TestUserGrain_SenderPID is the integration validation suite for the
// PID-in-payload wire-format design (ADR-011) and the Watch-based
// connection lifecycle (ADR-012). All subtests share a single in-process
// cluster — protoactor's automanaged discovery seems to leave stale port
// state across rapid cluster restarts, so booting one cluster per test
// made the suite flaky in `make test`. Sharing keeps each test
// independent (distinct user IDs) without paying the startup cost
// repeatedly.
//
// History: an earlier design attempted to capture the connection PID from
// `ctx.Sender()` inside RegisterConnection. That broke in production
// because cluster RPC arrives on a transient future PID, not the calling
// actor — fan-outs dead-lettered silently. The original regression test
// in this file proved that design wrong; these subtests now exercise the
// PID-in-payload design and assert delivery.
func TestUserGrain_SenderPID(t *testing.T) {
	c := sharedCluster

	t.Run("Delivery — PID-in-payload reaches the registered actor", func(t *testing.T) {
		const userID = "12"
		r, pid := spawnReceiver(t, c)
		registerSelfWithPID(t, c, pid, r, userID)

		const senderName = "Alice Delivery"
		uc := userpb.GetUserGrainGrainClient(c, userID)
		fwdReq := &userpb.ForwardMessageRequest{
			Room: &commonpb.RoomRef{RoomId: "4", PublicCode: "G000000004"}, Sender: &commonpb.UserRef{Id: userID, Name: senderName}, Text: "hi", Timestamp: timestamppb.New(time.UnixMilli(1)),
		}
		if _, err := uc.ForwardMessage(fwdReq); err != nil {
			t.Fatalf("ForwardMessage via cluster: %v", err)
		}

		waitForReceived(t, r, 1)
		got := r.Received()
		if len(got) != 1 {
			t.Fatalf("receiving actor got %d messages, want 1 (PID-in-payload design must deliver)", len(got))
		}
		if got[0].GetText() != "hi" || got[0].GetSender().GetId() != userID || got[0].GetSender().GetName() != senderName {
			t.Errorf("payload mismatch: got %+v, want text=hi sender id=%s name=%s", got[0], userID, senderName)
		}
	})

	t.Run("MultiDeviceDelivery — both registered actors receive the fan-out", func(t *testing.T) {
		const userID = "13"
		rA, pidA := spawnReceiver(t, c)
		rB, pidB := spawnReceiver(t, c)
		registerSelfWithPID(t, c, pidA, rA, userID)
		registerSelfWithPID(t, c, pidB, rB, userID)

		uc := userpb.GetUserGrainGrainClient(c, userID)
		fwdReq := &userpb.ForwardMessageRequest{
			Room: &commonpb.RoomRef{RoomId: "4", PublicCode: "G000000004"}, Sender: &commonpb.UserRef{Id: userID, Name: "Alice Multi"}, Text: "multi-device", Timestamp: timestamppb.New(time.UnixMilli(42)),
		}
		if _, err := uc.ForwardMessage(fwdReq); err != nil {
			t.Fatalf("ForwardMessage via cluster: %v", err)
		}

		waitForReceived(t, rA, 1)
		waitForReceived(t, rB, 1)
		if got := len(rA.Received()); got != 1 {
			t.Errorf("connection A received %d messages, want 1", got)
		}
		if got := len(rB.Received()); got != 1 {
			t.Errorf("connection B received %d messages, want 1", got)
		}
	})

	t.Run("WatchEvictsOnTermination — Terminated drops the entry so fan-out stops", func(t *testing.T) {
		const userID = "14"
		// Two receivers: A is poisoned mid-test to fire Terminated at the
		// User grain; B stays alive and must keep receiving fan-outs.
		rA, pidA := spawnReceiver(t, c)
		rB, pidB := spawnReceiver(t, c)
		registerSelfWithPID(t, c, pidA, rA, userID)
		registerSelfWithPID(t, c, pidB, rB, userID)

		// Poison A and wait for it to actually stop. The grain's Watch
		// turns this into a death-watch notification that ReceiveDefault
		// processes; we then poll fan-out rounds rather than sleeping for
		// a fixed window.
		if err := c.ActorSystem.Root.PoisonFuture(pidA).Wait(); err != nil {
			t.Fatalf("PoisonFuture for A: %v", err)
		}

		// Each ForwardMessage call goes through the grain's mailbox;
		// the death-watch notification for A is queued ahead of follow-up
		// RPCs once Poison has been applied, so within a small number of
		// rounds the grain must stop trying to send to A — proven by a full
		// round producing no dead letter at A while B still receives.
		fwdReq := &userpb.ForwardMessageRequest{
			Room: &commonpb.RoomRef{RoomId: "4", PublicCode: "G000000004"}, Sender: &commonpb.UserRef{Id: userID, Name: "Alice Watch"}, Text: "after-evict", Timestamp: timestamppb.New(time.UnixMilli(99)),
		}
		forwardUntilEvicted(t, c, userID, fwdReq, pidA, rB, 0)
	})

	t.Run("WatchOnDeadPID — registering an already-stopped PID evicts, not retains", func(t *testing.T) {
		const userID = "15"

		// Stop the connection actor BEFORE its PID is registered. Watching a
		// dead PID makes protoactor reply Terminated immediately, so this
		// probes the register-then-instant-Terminated path rather than a
		// connection dying later (covered by WatchEvictsOnTermination).
		_, deadPID := spawnReceiver(t, c)
		if err := c.ActorSystem.Root.PoisonFuture(deadPID).Wait(); err != nil {
			t.Fatalf("PoisonFuture for dead receiver: %v", err)
		}

		// The dead actor cannot register itself, so carry its PID from the
		// test. Registration itself reports success — the grain cannot know
		// liveness synchronously; the Watch reply is what settles it.
		uc := userpb.GetUserGrainGrainClient(c, userID)
		resp, err := uc.RegisterConnection(&userpb.RegisterConnectionRequest{
			RequesterPid: &userpb.PID{Address: deadPID.GetAddress(), Id: deadPID.GetId()},
		})
		if err != nil {
			t.Fatalf("RegisterConnection with dead PID: %v", err)
		}
		if ed := resp.GetError(); ed != nil {
			t.Fatalf("RegisterConnection with dead PID failed: code=%d status=%q msg=%q",
				ed.GetCode(), ed.GetStatus(), ed.GetMessage())
		}

		// A live connection registered afterwards must be unaffected.
		rLive, livePID := spawnReceiver(t, c)
		registerSelfWithPID(t, c, livePID, rLive, userID)

		// The Terminated reply is queued through the grain's mailbox, so an
		// early fan-out may still attempt the dead PID once; within a few
		// rounds the entry must be gone.
		fwdReq := &userpb.ForwardMessageRequest{
			Room: &commonpb.RoomRef{RoomId: "4", PublicCode: "G000000004"}, Sender: &commonpb.UserRef{Id: userID, Name: "Alice DeadWatch"}, Text: "after-dead-register", Timestamp: timestamppb.New(time.UnixMilli(7)),
		}
		forwardUntilEvicted(t, c, userID, fwdReq, deadPID, rLive, 0)
	})
}

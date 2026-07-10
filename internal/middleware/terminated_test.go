package middleware_test

import (
	"testing"

	"github.com/asynkron/protoactor-go/actor"

	"github.com/oklahomer/blabby/internal/middleware"
)

func TestTranslateTerminated_RewrapsForReceiveDefault(t *testing.T) {
	who := actor.NewPID("addr", "conn-1")
	in := &actor.MessageEnvelope{
		Message: &actor.Terminated{Who: who, Why: actor.TerminatedReason_NotFound},
		Sender:  actor.NewPID("addr", "sender"),
	}

	var got *actor.MessageEnvelope
	next := func(_ actor.ReceiverContext, env *actor.MessageEnvelope) { got = env }
	middleware.TranslateTerminated()(next)(newFakeCtx("15"), in)

	if got == nil {
		t.Fatal("middleware did not call next")
	}
	wt, ok := got.Message.(*middleware.WatchedTerminated)
	if !ok {
		t.Fatalf("next received %T, want *middleware.WatchedTerminated", got.Message)
	}
	if !wt.Who.Equal(who) {
		t.Errorf("Who = %v, want %v", wt.Who, who)
	}
	if wt.Why != actor.TerminatedReason_NotFound {
		t.Errorf("Why = %v, want NotFound", wt.Why)
	}
	if got.Sender != in.Sender {
		t.Errorf("Sender = %v, want the original envelope's sender", got.Sender)
	}
}

func TestTranslateTerminated_PassesOtherMessagesUntouched(t *testing.T) {
	in := &actor.MessageEnvelope{Message: &actor.Started{}}

	var got *actor.MessageEnvelope
	next := func(_ actor.ReceiverContext, env *actor.MessageEnvelope) { got = env }
	middleware.TranslateTerminated()(next)(newFakeCtx("15"), in)

	if got != in {
		t.Errorf("next received a different envelope %v, want the original passed through", got)
	}
}

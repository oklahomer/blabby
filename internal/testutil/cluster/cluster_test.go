package clustertest

import (
	"testing"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
)

func TestStart_NoKinds(t *testing.T) {
	c := Start(t)

	if c == nil {
		t.Fatal("Start returned nil cluster")
	}
	if c.ActorSystem == nil {
		t.Errorf("expected non-nil ActorSystem on cluster")
	}
}

func TestStart_WithDummyKind(t *testing.T) {
	dummy := cluster.NewKind("DummyKind", actor.PropsFromFunc(func(actor.Context) {}))

	c := Start(t, dummy)

	if c == nil {
		t.Fatal("Start returned nil cluster")
	}
	kinds := c.GetClusterKinds()
	found := false
	for _, k := range kinds {
		if k == "DummyKind" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected DummyKind to be registered, got %v", kinds)
	}
}

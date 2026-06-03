package clusterboot

import "testing"

// TestBuildConstructsCluster exercises both provider branches of Build. Build
// only constructs the cluster (StartMember/StartClient bind ports later), so it
// is safe to call in a unit test without starting or shutting down anything.
func TestBuildConstructsCluster(t *testing.T) {
	tests := []struct {
		name string
		cc   Config
	}{
		{
			name: "single-node",
			cc:   Config{bindHost: defaultClusterHost, discoveryPort: defaultDiscoveryPort},
		},
		{
			name: "multi-node with advertised host",
			cc: Config{
				bindHost:       defaultClusterHost,
				bindPort:       8091,
				advertisedHost: "127.0.0.1:8091",
				discoveryPort:  defaultDiscoveryPort,
				seeds:          []string{"127.0.0.1:6330"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Build(tc.cc, Kinds(nil)...)
			if c == nil {
				t.Fatal("Build returned nil")
			}
			if c.ActorSystem == nil {
				t.Fatal("Build returned a cluster without an actor system")
			}
		})
	}
}

// TestSubscribeTopologyLogging confirms the subscription is established on the
// built cluster's EventStream.
func TestSubscribeTopologyLogging(t *testing.T) {
	c := Build(Config{bindHost: defaultClusterHost, discoveryPort: defaultDiscoveryPort}, Kinds(nil)...)

	sub := SubscribeTopologyLogging(c)
	if sub == nil {
		t.Fatal("SubscribeTopologyLogging returned nil")
	}
	c.ActorSystem.EventStream.Unsubscribe(sub)
}

func TestKindsRegistersUserAndRoom(t *testing.T) {
	kinds := Kinds(nil)

	got := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		if k == nil {
			t.Fatal("Kinds returned a nil kind")
		}
		got[k.Kind] = true
	}

	for _, want := range []string{"UserGrain", "RoomGrain"} {
		if !got[want] {
			t.Errorf("Kinds missing %q kind; got %v", want, got)
		}
	}
	if len(kinds) != 2 {
		t.Errorf("Kinds returned %d kinds, want 2", len(kinds))
	}
}

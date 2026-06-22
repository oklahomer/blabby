package clusterboot

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

// stubRoomPublicCode is a valid bare 10-symbol Crockford public_code used by the
// test loader so the Join flow's public-code parse (User grain) succeeds.
const stubRoomPublicCode = "G000000004"

// activeAnyRoomLoader is a room.RoomLoader that reports every id as an active
// room, so cluster tests can activate Room grains for arbitrary probed
// identities (see findIdentityOn) without a database. It supplies a valid public
// code so the RoomRef survives the User grain's boundary parse on Join.
type activeAnyRoomLoader struct{}

func (activeAnyRoomLoader) LoadRoom(_ context.Context, roomID id.RoomID) (domain.RoomRef, error) {
	code, err := id.ParsePublicCode(stubRoomPublicCode)
	if err != nil {
		return domain.RoomRef{}, err
	}
	return domain.RoomRef{
		ID:         roomID,
		PublicCode: code,
		Name:       "Room " + roomID.String(),
		Status:     domain.RoomStatusActive,
	}, nil
}

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
			c := Build(tc.cc, Kinds(nil, activeAnyRoomLoader{})...)
			if c.ActorSystem == nil {
				t.Fatal("Build returned a cluster without an actor system")
			}
		})
	}
}

// TestSubscribeTopologyLogging confirms the subscription is established on the
// built cluster's EventStream.
func TestSubscribeTopologyLogging(t *testing.T) {
	c := Build(Config{bindHost: defaultClusterHost, discoveryPort: defaultDiscoveryPort}, Kinds(nil, activeAnyRoomLoader{})...)

	sub := SubscribeTopologyLogging(c)
	if sub == nil {
		t.Fatal("SubscribeTopologyLogging returned nil")
	}
	c.ActorSystem.EventStream.Unsubscribe(sub)
}

func TestKindsRegistersUserAndRoom(t *testing.T) {
	kinds := Kinds(nil, activeAnyRoomLoader{})

	got := make(map[string]bool, len(kinds))
	for _, k := range kinds {
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

// TestShutdownClientDoesNotPanic starts a real cluster client and tears it down
// via ShutdownClient. cluster.Cluster.Shutdown would nil-panic here (a client
// never starts the gossiper); ShutdownClient must not. The test reaching the
// end without a panic is the assertion.
func TestShutdownClientDoesNotPanic(t *testing.T) {
	clusterPort := freeTCPPort(t)
	discoveryPort := freeTCPPort(t)
	cc, err := newClusterConfig(
		"127.0.0.1",
		clusterPort,
		net.JoinHostPort("127.0.0.1", strconv.Itoa(clusterPort)),
		discoveryPort,
		net.JoinHostPort("127.0.0.1", strconv.Itoa(discoveryPort)),
	)
	if err != nil {
		t.Fatalf("newClusterConfig: %v", err)
	}

	c := Build(cc) // a client registers no kinds
	c.StartClient()
	ShutdownClient(c)
}

// freeTCPPort asks the kernel for an unused loopback TCP port. There is a small
// TOCTOU window before the cluster binds it, which is acceptable for a test.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

//go:build !race

package clusterboot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/eventstream"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/persistence/workerlease"
)

const (
	multiMemberTimeout  = 20 * time.Second
	multiMemberPoll     = 100 * time.Millisecond
	memberShutdownLimit = 10 * time.Second
)

type testGrainKind struct {
	name     string
	activate func(*cluster.Cluster, string) error
}

type testMember struct {
	cluster     *cluster.Cluster
	topologies  chan *cluster.ClusterTopology
	topologySub *eventstream.Subscription
	loggingSub  *eventstream.Subscription
}

type testClient struct {
	cluster     *cluster.Cluster
	topologies  chan *cluster.ClusterTopology
	topologySub *eventstream.Subscription
	loggingSub  *eventstream.Subscription
}

type connectionProbe struct {
	messages      chan *userpb.ForwardMessageRequest
	notifications chan *userpb.NotifyRoomEventRequest
}

func (p *connectionProbe) Receive(ctx actor.Context) {
	switch msg := ctx.Message().(type) {
	case *userpb.ForwardMessageRequest:
		p.messages <- msg
	case *userpb.NotifyRoomEventRequest:
		p.notifications <- msg
	}
}

type traceCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *traceCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *traceCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

type traceStep struct {
	event string
	attrs map[string]string
}

func TestMultiMemberDepartureAndReactivation(t *testing.T) {
	// This is a real end-to-end durability test: both members run the production
	// persistence adapters against a shared Postgres, so the headline claim —
	// membership survives a node loss — is proven against the real datastore, not
	// a fake. Skipped unless BLABBY_DATABASE_URL points at a reachable instance
	// with the schema + dev seed applied (e.g. `make up`); CI provisions one.
	dsn := strings.TrimSpace(os.Getenv("BLABBY_DATABASE_URL"))
	if dsn == "" {
		t.Skip("BLABBY_DATABASE_URL not set; skipping database integration test")
	}
	pool := openTestPool(t, dsn)
	seedDepartureFixtures(t, pool)

	members, rawSeeds := startTestMembers(t, 2, pool)
	gateway := startTestClient(t, rawSeeds, memberAddresses(members))
	client := gateway.cluster
	survivor := members[0].cluster

	userKind := testGrainKind{
		name: "UserGrain",
		activate: func(c *cluster.Cluster, identity string) error {
			_, err := userpb.GetUserGrainGrainClient(c, identity).
				GetJoinedRooms(&userpb.GetJoinedRoomsRequest{})
			return err
		},
	}
	roomKind := testGrainKind{
		name: "RoomGrain",
		activate: func(c *cluster.Cluster, identity string) error {
			_, err := roompb.GetRoomGrainGrainClient(c, identity).
				Leave(&roompb.LeaveRequest{UserId: "999999"})
			return err
		},
	}

	const victim = 1
	recoveryUserID := findIdentityOn(t, client, members, victim, 1000, userKind)
	routingUserID := findIdentityOn(t, client, members, 0, 2000, userKind)
	roomID := findIdentityOn(t, client, members, victim, 3000, roomKind)

	recoveryBefore, recoveryBeforePID := spawnConnectionProbe(client)
	routingConnection, routingConnectionPID := spawnConnectionProbe(client)
	requireCall(t, "register recovery connection", func() error {
		return registerConnection(client, recoveryUserID, recoveryBeforePID)
	})
	requireCall(t, "register routing connection", func() error {
		return registerConnection(client, routingUserID, routingConnectionPID)
	})
	requireCall(t, "join recovery user to room", func() error {
		return joinRoom(client, recoveryUserID, roomID)
	})
	requireCall(t, "join routing user to room", func() error {
		return joinRoom(client, routingUserID, roomID)
	})
	assertRoomEvent(t, routingConnection, roomID, routingUserID, userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED)

	roomsBefore := getJoinedRoomsEventually(t, client, recoveryUserID)
	if len(roomsBefore) != 1 || roomsBefore[0] != roomID {
		t.Fatalf("recovery user rooms before departure = %v, want [%s]", roomsBefore, roomID)
	}

	requireCall(t, "send baseline room message", func() error {
		return sendMessage(client, routingUserID, roomID, "before-departure")
	})
	assertMessage(t, recoveryBefore, "before-departure")
	assertMessage(t, routingConnection, "before-departure")

	trace := startTraceCapture(t)
	victimAddress := members[victim].cluster.ActorSystem.Address()
	if err := shutdownCluster(members[victim].cluster); err != nil {
		t.Fatalf("depart member %d: %v", victim, err)
	}
	members[victim].cluster = nil
	waitForTopology(t, members[0].topologies, []string{survivor.ActorSystem.Address()})
	waitForTopology(t, gateway.topologies, []string{survivor.ActorSystem.Address()})

	t.Run("User reactivation and reconnect", func(t *testing.T) {
		// The reactivated recovery user re-hydrates its joined rooms from
		// DB-authoritative membership: the room it joined before the departure is
		// still there, no re-join required.
		roomsAfter := getJoinedRoomsEventually(t, client, recoveryUserID)
		if len(roomsAfter) != 1 || roomsAfter[0] != roomID {
			t.Fatalf("reactivated recovery user rooms = %v, want [%s] (membership persisted)", roomsAfter, roomID)
		}
		assertPlacement(t, client, recoveryUserID, userKind.name, survivor.ActorSystem.Address())

		recoveryAfter, recoveryAfterPID := spawnConnectionProbe(client)
		requireCall(t, "register replacement recovery connection", func() error {
			return registerConnection(client, recoveryUserID, recoveryAfterPID)
		})
		bodyMarker := "user-recovery-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		requireCall(t, "forward after user reactivation", func() error {
			return forwardMessage(client, recoveryUserID, bodyMarker)
		})
		assertMessage(t, recoveryAfter, bodyMarker)
		assertNoMessage(t, recoveryBefore, "departed User grain retained its old connection")

		assertTrace(t, trace.lines(t),
			traceStep{event: "server.cluster.member_left", attrs: map[string]string{"node_address": victimAddress}},
			traceStep{event: "grain.activated", attrs: map[string]string{"grain_type": userKind.name, "grain_id": recoveryUserID}},
			traceStep{event: "user.connection.registered", attrs: map[string]string{"grain_id": recoveryUserID}},
			traceStep{event: "grain.fanout", attrs: map[string]string{"grain_type": userKind.name, "grain_id": recoveryUserID}},
		)
		assertLogOmits(t, trace, bodyMarker)
	})

	t.Run("Room reactivation and routing", func(t *testing.T) {
		routingRooms := getJoinedRoomsEventually(t, client, routingUserID)
		if len(routingRooms) != 1 || routingRooms[0] != roomID {
			t.Fatalf("surviving routing user rooms = %v, want [%s]", routingRooms, roomID)
		}

		requireEventually(t, "activate Room grain after departure", func() error {
			return roomKind.activate(client, roomID)
		})
		assertPlacement(t, client, roomID, roomKind.name, survivor.ActorSystem.Address())

		// The reactivated Room grain reloaded its member set from the DB, so the
		// routing user is still a member: it can post WITHOUT re-joining and the
		// message fans out to its connection.
		bodyMarker := "room-recovery-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		requireCall(t, "send after room reactivation (no re-join)", func() error {
			return sendMessage(client, routingUserID, roomID, bodyMarker)
		})
		assertMessage(t, routingConnection, bodyMarker)
		assertNoAdditionalMessage(t, routingConnection, bodyMarker)

		assertTrace(t, trace.lines(t),
			traceStep{event: "server.cluster.member_left", attrs: map[string]string{"node_address": victimAddress}},
			traceStep{event: "grain.activated", attrs: map[string]string{"grain_type": roomKind.name, "grain_id": roomID}},
			traceStep{event: "room.message.posted", attrs: map[string]string{"grain_id": roomID}},
			traceStep{event: "grain.fanout", attrs: map[string]string{"grain_type": roomKind.name, "grain_id": roomID}},
		)
		assertLogOmits(t, trace, bodyMarker)
	})
}

// --- database-backed grain wiring for the departure test ---

const (
	// departureUserSeedSQL seeds the users the placement probes reach (recovery
	// 1000+, routing 2000+) so membership/event writes satisfy their FKs into
	// service_user. public_code/mail/handle are derived from the id to stay unique.
	departureUserSeedSQL = `
INSERT INTO service_user (id, public_code, mail_address, handle, handle_norm, display_name, password_hash, status)
SELECT gs, lpad(gs::text, 10, '0'), 'u' || gs || '@departure.test', 'u' || gs, 'u' || gs,
       'User ' || gs, convert_to('x', 'UTF8'), 'active'
FROM (
    SELECT generate_series(1000, 1199) AS gs
    UNION ALL
    SELECT generate_series(2000, 2199) AS gs
) s
ON CONFLICT DO NOTHING`

	// departureRoomSeedSQL seeds active rooms covering the room probe range (3000+)
	// so the real room loader hydrates them on activation. created_by is the dev
	// seed's user 1.
	departureRoomSeedSQL = `
INSERT INTO room (id, public_code, display_name, created_by, status)
SELECT gs, lpad(gs::text, 10, '0'), 'Room ' || gs, 1, 'active'
FROM generate_series(3000, 3199) AS gs
ON CONFLICT DO NOTHING`
)

// openTestPool connects a pool to the test database and closes it on cleanup.
func openTestPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.NewPool(context.Background(), postgres.Config{
		DSN: dsn, MaxConns: 8, MaxConnIdleTime: time.Minute, MaxConnLifetime: time.Hour,
	})
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedDepartureFixtures seeds the users and rooms the placement probes reach so
// membership writes satisfy the room_membership FKs and the real room loader can
// hydrate the probed rooms. It clears prior test rows up front and again on
// cleanup so the test is re-runnable (seeded users/rooms are left in place;
// re-seeding is idempotent).
func seedDepartureFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if err := cleanDepartureRows(ctx, pool); err != nil {
		t.Fatalf("pre-clean fixtures: %v", err)
	}
	if _, err := pool.Exec(ctx, departureUserSeedSQL); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, departureRoomSeedSQL); err != nil {
		t.Fatalf("seed rooms: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanDepartureRows(context.Background(), pool); err != nil {
			t.Logf("cleanup fixtures: %v", err)
		}
	})
}

// cleanDepartureRows removes only the rows this test writes — memberships for the
// probed rooms and their derived timeline events — so a re-run starts clean.
func cleanDepartureRows(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `DELETE FROM event WHERE room_id BETWEEN 3000 AND 3199`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM room_membership WHERE room_id BETWEEN 3000 AND 3199`); err != nil {
		return err
	}
	return nil
}

// newDatabaseDeps builds one member's production grain dependencies over the
// shared pool: the real room/membership/joined-room adapters plus a per-member
// worker-lease manager, so the two members mint event ids under distinct worker
// ids. The manager is released on cleanup.
func newDatabaseDeps(t *testing.T, pool *pgxpool.Pool, member int) GrainDeps {
	t.Helper()
	manager, err := workerlease.NewManager(
		workerlease.NewRepo(pool),
		fmt.Sprintf("departure-test-member-%d", member),
	)
	if err != nil {
		t.Fatalf("build worker-lease manager %d: %v", member, err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("acquire worker lease %d: %v", member, err)
	}
	t.Cleanup(manager.Stop)

	return GrainDeps{
		Directory:   user.NewRepoDirectory(pool),
		RoomLoader:  room.NewRoomRepoLoader(pool),
		Membership:  room.NewMembershipStore(pool, manager),
		JoinedRooms: user.NewJoinedRoomLoader(pool),
	}
}

func startTestMembers(t *testing.T, count int, pool *pgxpool.Pool) ([]*testMember, string) {
	t.Helper()
	ports := reserveTestPorts(t, count*2)
	discoveryPorts := ports[:count]
	remotePorts := ports[count:]

	seeds := make([]string, count)
	for i, port := range discoveryPorts {
		seeds[i] = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	rawSeeds := strings.Join(seeds, ",")

	members := make([]*testMember, count)
	t.Cleanup(func() {
		for i := len(members) - 1; i >= 0; i-- {
			member := members[i]
			if member == nil || member.cluster == nil {
				continue
			}
			member.cluster.ActorSystem.EventStream.Unsubscribe(member.topologySub)
			member.cluster.ActorSystem.EventStream.Unsubscribe(member.loggingSub)
			if err := shutdownCluster(member.cluster); err != nil {
				t.Errorf("cleanup member %d: %v", i, err)
			}
		}
	})

	for i := range members {
		advertisedHost := net.JoinHostPort("127.0.0.1", strconv.Itoa(remotePorts[i]))
		cc, err := newClusterConfig(
			"127.0.0.1",
			remotePorts[i],
			advertisedHost,
			discoveryPorts[i],
			rawSeeds,
		)
		if err != nil {
			t.Fatalf("build member %d config: %v", i, err)
		}

		member := &testMember{
			cluster:    Build(cc, Kinds(newDatabaseDeps(t, pool, i))...),
			topologies: make(chan *cluster.ClusterTopology, 16),
		}
		member.loggingSub = SubscribeTopologyLogging(member.cluster)
		member.topologySub = member.cluster.ActorSystem.EventStream.Subscribe(func(evt any) {
			if topology, ok := evt.(*cluster.ClusterTopology); ok {
				member.topologies <- topology
			}
		})
		member.cluster.StartMember()
		members[i] = member
		waitForDiscovery(t, discoveryPorts[i])
	}

	expectedAddresses := memberAddresses(members)
	for _, member := range members {
		waitForTopology(t, member.topologies, expectedAddresses)
	}
	return members, rawSeeds
}

func startTestClient(t *testing.T, rawSeeds string, expectedAddresses []string) *testClient {
	t.Helper()
	ports := reserveTestPorts(t, 2)
	remotePort := ports[0]
	discoveryPort := ports[1]
	advertisedHost := net.JoinHostPort("127.0.0.1", strconv.Itoa(remotePort))
	cc, err := newClusterConfig(
		"127.0.0.1",
		remotePort,
		advertisedHost,
		discoveryPort,
		rawSeeds,
	)
	if err != nil {
		t.Fatalf("build client config: %v", err)
	}

	client := &testClient{
		cluster:    Build(cc),
		topologies: make(chan *cluster.ClusterTopology, 16),
	}
	client.loggingSub = SubscribeTopologyLogging(client.cluster)
	client.topologySub = client.cluster.ActorSystem.EventStream.Subscribe(func(evt any) {
		if topology, ok := evt.(*cluster.ClusterTopology); ok {
			client.topologies <- topology
		}
	})
	t.Cleanup(func() {
		client.cluster.ActorSystem.EventStream.Unsubscribe(client.topologySub)
		client.cluster.ActorSystem.EventStream.Unsubscribe(client.loggingSub)
		ShutdownClient(client.cluster)
	})

	client.cluster.StartClient()
	waitForTopology(t, client.topologies, expectedAddresses)
	return client
}

func memberAddresses(members []*testMember) []string {
	addresses := make([]string, len(members))
	for i, member := range members {
		addresses[i] = member.cluster.ActorSystem.Address()
	}
	return addresses
}

func reserveTestPorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	for range count {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for _, open := range listeners {
				_ = open.Close()
			}
			t.Fatalf("reserve test port: %v", err)
		}
		listeners = append(listeners, listener)
	}

	ports := make([]int, len(listeners))
	for i, listener := range listeners {
		ports[i] = listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatalf("release reserved test port %d: %v", ports[i], err)
		}
	}
	return ports
}

func waitForDiscovery(t *testing.T, port int) {
	t.Helper()
	client := &http.Client{Timeout: multiMemberPoll}
	url := fmt.Sprintf("http://127.0.0.1:%d/_health", port)
	requireEventually(t, fmt.Sprintf("discovery endpoint %d", port), func() error {
		resp, err := client.Get(url)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %s", resp.Status)
		}
		return nil
	})
}

func waitForTopology(t *testing.T, events <-chan *cluster.ClusterTopology, expected []string) {
	t.Helper()
	want := make(map[string]struct{}, len(expected))
	for _, address := range expected {
		want[address] = struct{}{}
	}

	timer := time.NewTimer(multiMemberTimeout)
	defer timer.Stop()
	for {
		select {
		case topology := <-events:
			if topologyMatches(topology, want) {
				return
			}
		case <-timer.C:
			t.Fatalf("topology did not converge to %v within %s", expected, multiMemberTimeout)
		}
	}
}

func topologyMatches(topology *cluster.ClusterTopology, expected map[string]struct{}) bool {
	if len(topology.GetMembers()) != len(expected) {
		return false
	}
	for _, member := range topology.GetMembers() {
		if _, ok := expected[member.Address()]; !ok {
			return false
		}
	}
	return true
}

func shutdownCluster(c *cluster.Cluster) error {
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("shutdown panic: %v\n%s", recovered, debug.Stack())
			}
			done <- err
		}()
		c.Shutdown(true)
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(memberShutdownLimit):
		return fmt.Errorf("shutdown timed out after %s", memberShutdownLimit)
	}
}

// findIdentityOn searches for a grain identity that the cluster places on the
// target member, probing numeric Snowflake-shaped identities from base upward so
// the user/room grains parse them (ids are numeric now). Each caller passes a
// distinct base to keep the discovered user/room identities apart.
func findIdentityOn(
	t *testing.T,
	client *cluster.Cluster,
	members []*testMember,
	target int,
	base int64,
	kind testGrainKind,
) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		identity := strconv.FormatInt(base+int64(i), 10)
		requireEventually(t, fmt.Sprintf("activate %s/%s", kind.name, identity), func() error {
			return kind.activate(client, identity)
		})

		pid := waitForPlacement(t, client, identity, kind.name)
		if pid.GetAddress() == members[target].cluster.ActorSystem.Address() {
			return identity
		}
	}
	t.Fatalf("no %s identity from base %d was placed on member %d", kind.name, base, target)
	return ""
}

func waitForPlacement(t *testing.T, client *cluster.Cluster, identity, kind string) *actor.PID {
	t.Helper()
	var pid *actor.PID
	requireEventually(t, fmt.Sprintf("resolve %s/%s placement", kind, identity), func() error {
		pid = client.Get(identity, kind)
		if pid == nil {
			return fmt.Errorf("placement unavailable")
		}
		return nil
	})
	return pid
}

func assertPlacement(t *testing.T, client *cluster.Cluster, identity, kind, expectedAddress string) {
	t.Helper()
	pid := waitForPlacement(t, client, identity, kind)
	if pid.GetAddress() != expectedAddress {
		t.Fatalf("%s/%s placed at %s, want %s", kind, identity, pid.GetAddress(), expectedAddress)
	}
}

func spawnConnectionProbe(client *cluster.Cluster) (*connectionProbe, *actor.PID) {
	probe := &connectionProbe{
		messages:      make(chan *userpb.ForwardMessageRequest, 4),
		notifications: make(chan *userpb.NotifyRoomEventRequest, 4),
	}
	pid := client.ActorSystem.Root.Spawn(actor.PropsFromProducer(func() actor.Actor { return probe }))
	return probe, pid
}

func registerConnection(member *cluster.Cluster, userID string, pid *actor.PID) error {
	resp, err := userpb.GetUserGrainGrainClient(member, userID).
		RegisterConnection(&userpb.RegisterConnectionRequest{
			RequesterPid: &userpb.PID{Address: pid.GetAddress(), Id: pid.GetId()},
		})
	if err != nil {
		return err
	}
	if detail := resp.GetError(); detail != nil {
		return fmt.Errorf("%s: %s", detail.GetStatus(), detail.GetMessage())
	}
	return nil
}

func joinRoom(member *cluster.Cluster, userID, roomID string) error {
	resp, err := userpb.GetUserGrainGrainClient(member, userID).
		JoinRoom(&userpb.JoinRoomRequest{RoomId: roomID})
	if err != nil {
		return err
	}
	if detail := resp.GetError(); detail != nil {
		return fmt.Errorf("%s: %s", detail.GetStatus(), detail.GetMessage())
	}
	return nil
}

func sendMessage(member *cluster.Cluster, userID, roomID, text string) error {
	resp, err := userpb.GetUserGrainGrainClient(member, userID).
		SendMessage(&userpb.SendMessageRequest{RoomId: roomID, Text: text})
	if err != nil {
		return err
	}
	if detail := resp.GetError(); detail != nil {
		return fmt.Errorf("%s: %s", detail.GetStatus(), detail.GetMessage())
	}
	return nil
}

func forwardMessage(member *cluster.Cluster, userID, text string) error {
	_, err := userpb.GetUserGrainGrainClient(member, userID).
		ForwardMessage(&userpb.ForwardMessageRequest{
			Room:      &commonpb.RoomRef{RoomId: "4", PublicCode: "G000000004"},
			Sender:    &commonpb.UserRef{Id: "2", Name: "Sender"},
			Text:      text,
			Timestamp: timestamppb.New(time.UnixMilli(1)),
		})
	return err
}

func getJoinedRoomsEventually(t *testing.T, member *cluster.Cluster, userID string) []string {
	t.Helper()
	var roomIDs []string
	requireEventually(t, "get joined rooms for "+userID, func() error {
		resp, err := userpb.GetUserGrainGrainClient(member, userID).
			GetJoinedRooms(&userpb.GetJoinedRoomsRequest{})
		if err != nil {
			return err
		}
		ids := make([]string, len(resp.GetRooms()))
		for i, room := range resp.GetRooms() {
			ids[i] = room.GetRoomId()
		}
		roomIDs = ids
		return nil
	})
	return roomIDs
}

func assertMessage(t *testing.T, probe *connectionProbe, wantText string) {
	t.Helper()
	select {
	case msg := <-probe.messages:
		if msg.GetText() != wantText {
			t.Fatalf("connection message text = %q, want %q", msg.GetText(), wantText)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for connection message %q", wantText)
	}
}

func assertRoomEvent(
	t *testing.T,
	probe *connectionProbe,
	wantRoomID string,
	wantUserID string,
	wantType userpb.RoomEventType,
) {
	t.Helper()
	select {
	case notification := <-probe.notifications:
		if notification.GetRoom().GetRoomId() != wantRoomID ||
			notification.GetUser().GetId() != wantUserID ||
			notification.GetEventType() != wantType {
			t.Fatalf(
				"connection room event = {room_id:%q user_id:%q type:%s}, want {room_id:%q user_id:%q type:%s}",
				notification.GetRoom().GetRoomId(),
				notification.GetUser().GetId(),
				notification.GetEventType(),
				wantRoomID,
				wantUserID,
				wantType,
			)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for room event {room_id:%q user_id:%q type:%s}", wantRoomID, wantUserID, wantType)
	}
}

func assertNoMessage(t *testing.T, probe *connectionProbe, reason string) {
	t.Helper()
	select {
	case msg := <-probe.messages:
		t.Fatalf("%s: received %q", reason, msg.GetText())
	case <-time.After(300 * time.Millisecond):
	}
}

func assertNoAdditionalMessage(t *testing.T, probe *connectionProbe, deliveredText string) {
	t.Helper()
	select {
	case msg := <-probe.messages:
		if msg.GetText() == deliveredText {
			t.Fatalf("connection received message %q more than once", deliveredText)
		}
		t.Fatalf("connection received unexpected additional message %q after %q", msg.GetText(), deliveredText)
	case <-time.After(300 * time.Millisecond):
	}
}

func startTraceCapture(t *testing.T) *traceCapture {
	t.Helper()
	capture := &traceCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(capture, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return capture
}

func (c *traceCapture) lines(t *testing.T) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(c.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("parse recovery log line %q: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

func assertTrace(t *testing.T, lines []map[string]any, steps ...traceStep) {
	t.Helper()
	next := 0
	for _, step := range steps {
		found := -1
		for i := next; i < len(lines); i++ {
			if lineMatches(lines[i], step) {
				found = i
				break
			}
		}
		if found < 0 {
			t.Errorf("recovery trace missing %q after line %d", step.event, next-1)
			continue
		}
		next = found + 1
	}
}

func lineMatches(line map[string]any, step traceStep) bool {
	if line["msg"] != step.event {
		return false
	}
	for key, want := range step.attrs {
		if line[key] != want {
			return false
		}
	}
	return true
}

func assertLogOmits(t *testing.T, capture *traceCapture, marker string) {
	t.Helper()
	if strings.Contains(capture.String(), marker) {
		t.Errorf("recovery logs leaked message body %q", marker)
	}
}

func requireCall(t *testing.T, description string, call func() error) {
	t.Helper()
	if err := call(); err != nil {
		t.Fatalf("%s: %v", description, err)
	}
}

func requireEventually(t *testing.T, description string, action func() error) {
	t.Helper()
	deadline := time.Now().Add(multiMemberTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := action(); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(multiMemberPoll)
	}
	t.Fatalf("%s did not succeed within %s: %v", description, multiMemberTimeout, lastErr)
}

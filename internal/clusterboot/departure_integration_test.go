//go:build !race

package clusterboot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/eventstream"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
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
	members, rawSeeds := startTestMembers(t, 2)
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
		roomsAfter := getJoinedRoomsEventually(t, client, recoveryUserID)
		if len(roomsAfter) != 0 {
			t.Fatalf("reactivated recovery user rooms = %v, want empty state", roomsAfter)
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

		requireCall(t, "rejoin routing user after room reactivation", func() error {
			return joinRoom(client, routingUserID, roomID)
		})
		assertRoomEvent(t, routingConnection, roomID, routingUserID, userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED)

		bodyMarker := "room-recovery-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		requireCall(t, "send after room reactivation", func() error {
			return sendMessage(client, routingUserID, roomID, bodyMarker)
		})
		assertMessage(t, routingConnection, bodyMarker)
		assertNoAdditionalMessage(t, routingConnection, bodyMarker)

		assertTrace(t, trace.lines(t),
			traceStep{event: "server.cluster.member_left", attrs: map[string]string{"node_address": victimAddress}},
			traceStep{event: "grain.activated", attrs: map[string]string{"grain_type": roomKind.name, "grain_id": roomID}},
			traceStep{event: "room.member.joined", attrs: map[string]string{"grain_id": roomID}},
			traceStep{event: "grain.fanout", attrs: map[string]string{"grain_type": roomKind.name, "grain_id": roomID}},
		)
		assertLogOmits(t, trace, bodyMarker)
	})
}

func startTestMembers(t *testing.T, count int) ([]*testMember, string) {
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
			cluster:    Build(cc, Kinds(nil, activeAnyRoomLoader{})...),
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
			RoomId:    "4",
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
		if notification.GetRoomId() != wantRoomID ||
			notification.GetUser().GetId() != wantUserID ||
			notification.GetEventType() != wantType {
			t.Fatalf(
				"connection room event = {room_id:%q user_id:%q type:%s}, want {room_id:%q user_id:%q type:%s}",
				notification.GetRoomId(),
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

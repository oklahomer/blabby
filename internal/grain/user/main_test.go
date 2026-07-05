package user_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/cluster"
	"google.golang.org/protobuf/types/known/timestamppb"

	roompb "github.com/oklahomer/blabby/gen/room"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/id"
	clustertest "github.com/oklahomer/blabby/internal/testutil/cluster"
)

// sharedCluster is a single in-process cluster shared by every test in this
// package that needs a real actor system (integration_test.go and
// sender_pid_test.go). It is started once in TestMain and torn down after
// all tests complete.
//
// Why share?  protoactor-go's remote.Start calls grpclog.SetLoggerV2,
// which is a process-global write. Booting two clusters back-to-back in
// the same `go test` invocation races against the prior cluster's still-
// running grpc balancer goroutine reading the same global. Sharing a
// single cluster sidesteps the race entirely without compromising test
// isolation — each test uses a distinct user identity so grain state
// never overlaps.
//
// stubRoomCounters tracks Room grain calls for the integration test that
// asserts cluster routing reaches the stub.
var (
	sharedCluster      *cluster.Cluster
	stubRoomJoinCount  int64
	stubRoomLeaveCount int64
	stubRoomPostCount  int64

	// stubRoomJoinUserName / stubRoomPostUserName hold the display name the
	// stub Room grain last saw on a Join/PostMessage UserRef. The
	// integration test reads them to confirm the seeded name propagated.
	stubRoomJoinUserName atomic.Pointer[string]
	stubRoomPostUserName atomic.Pointer[string]
)

// stubPostTimestamp is the deterministic timestamp the stub Room grain
// returns for every PostMessage. Asserted by the integration test.
var stubPostTimestamp = time.UnixMilli(9999)

// seededDisplayName is the display name the fake directory hands back for
// the integration test's user. The integration test asserts it propagated
// all the way to the stub Room grain's JoinRequest/PostMessageRequest,
// proving NewKind's directory seeding survives the full cluster round-trip.
const seededDisplayName = "Alice Example"

// fakeDirectory is a tiny user.Directory that returns seededDisplayName for
// every identity. Injected into user.NewKind so cluster-using tests can
// assert the seeded name flows into the Room grain.
type fakeDirectory struct{}

func (fakeDirectory) Resolve(_ context.Context, uid id.UserID) (id.UserRef, error) {
	code, _ := id.NewPublicCode()
	return id.NewUserRef(uid, code, seededDisplayName)
}

func TestMain(m *testing.M) {
	// Stub Room grain shared by all cluster-using tests. Counters are
	// reset by tests that care via atomic.StoreInt64.
	roomKind := roompb.NewRoomGrainKind(func() roompb.RoomGrain {
		return &stubRoomGrain{
			joinCount:    &stubRoomJoinCount,
			leaveCount:   &stubRoomLeaveCount,
			postCount:    &stubRoomPostCount,
			joinUserName: &stubRoomJoinUserName,
			postUserName: &stubRoomPostUserName,
			postResponse: &roompb.PostMessageResponse{
				Timestamp: timestamppb.New(stubPostTimestamp),
			},
		}
	}, time.Minute)
	userKind := user.NewKind(fakeDirectory{})

	// We need to call clustertest.Start with a *testing.T, but TestMain
	// runs before any test exists. Construct a minimal stand-in: the
	// helper only uses Helper, Cleanup, Fatalf, Errorf.
	t := &mainT{output: os.Stderr}

	// Run setup and tests inside one closure so the deferred cleanup runs
	// before os.Exit (which skips defers). Starting the cluster INSIDE the
	// closure means a Fatalf during bootstrap panics through runCleanups,
	// shutting down whatever the partially-started cluster registered rather
	// than leaking it; cleanup also runs if a test panics.
	exit := func() (code int) {
		defer func() {
			t.runCleanups()
			code = t.exitCode(code)
		}()
		sharedCluster = clustertest.Start(t, roomKind, userKind)
		return m.Run()
	}()
	os.Exit(exit)
}

// mainT is a minimal *testing.T-shaped value usable from TestMain. It
// satisfies the subset of methods that clustertest.Start invokes:
// Helper (no-op), Cleanup (recorded for end-of-suite execution),
// Fatalf (panics to abort TestMain), Errorf (records failure and logs).
type mainT struct {
	mu       sync.Mutex
	cleanups []func()
	failed   bool
	output   io.Writer
}

func (m *mainT) Helper() {}
func (m *mainT) Cleanup(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanups = append(m.cleanups, fn)
}

// Fatalf preserves the format args so the underlying error reaches the
// panic message; otherwise diagnosing TestMain setup failures requires
// reading source rather than the panic output.
func (m *mainT) Fatalf(format string, args ...any) {
	panic(fmt.Sprintf("TestMain setup failed: "+format, args...))
}

// Errorf records a package-level test failure and surfaces the diagnostic.
// TestMain checks the recorded state after cleanups so shutdown failures cannot
// leave a successful m.Run exit code unchanged.
func (m *mainT) Errorf(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed = true
	out := m.output
	if out == nil {
		out = os.Stderr
	}
	// Best-effort diagnostic; a failed write to the test's own output is not
	// actionable and must not mask the recorded failure above.
	_, _ = fmt.Fprintf(out, "[clustertest] "+format+"\n", args...)
}

func (m *mainT) exitCode(code int) int {
	m.mu.Lock()
	failed := m.failed
	m.mu.Unlock()
	if code == 0 && failed {
		return 1
	}
	return code
}

func (m *mainT) runCleanups() {
	// Snapshot under the lock and release before invoking so a cleanup
	// can call Cleanup itself without re-entering the mutex.
	m.mu.Lock()
	cleanups := m.cleanups
	m.cleanups = nil
	m.mu.Unlock()

	// LIFO order, like *testing.T. Each cleanup is wrapped in its own
	// recover so a panicking cleanup records failure without preventing later
	// cleanups (notably the cluster shutdown) from running.
	for i := len(cleanups) - 1; i >= 0; i-- {
		func(fn func()) {
			defer func() {
				if r := recover(); r != nil {
					m.Errorf("cleanup panicked: %v", r)
				}
			}()
			fn()
		}(cleanups[i])
	}
}

// resetStubRoomCounters zeroes the stub Room grain counters before a test
// that asserts on them.
func resetStubRoomCounters() {
	atomic.StoreInt64(&stubRoomJoinCount, 0)
	atomic.StoreInt64(&stubRoomLeaveCount, 0)
	atomic.StoreInt64(&stubRoomPostCount, 0)
}

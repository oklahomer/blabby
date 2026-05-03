package user_test

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/cluster"

	roompb "github.com/oklahomer/blabby/gen/room"
	"github.com/oklahomer/blabby/internal/grain/user"
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
)

// stubPostTimestamp is the deterministic timestamp the stub Room grain
// returns for every PostMessage. Asserted by the integration test.
const stubPostTimestamp int64 = 9999

func TestMain(m *testing.M) {
	// Stub Room grain shared by all cluster-using tests. Counters are
	// reset by tests that care via atomic.StoreInt64.
	roomKind := roompb.NewRoomGrainKind(func() roompb.RoomGrain {
		return &stubRoomGrain{
			joinCount:  &stubRoomJoinCount,
			leaveCount: &stubRoomLeaveCount,
			postCount:  &stubRoomPostCount,
			postResponse: &roompb.PostMessageResponse{
				Success:   true,
				Timestamp: stubPostTimestamp,
			},
		}
	}, time.Minute)
	userKind := user.NewKind()

	// We need to call clustertest.Start with a *testing.T, but TestMain
	// runs before any test exists. Construct a minimal stand-in: the
	// helper only uses Helper, Cleanup, Fatalf, Errorf.
	t := &mainT{}
	sharedCluster = clustertest.Start(t, roomKind, userKind)

	// Wrap m.Run in a closure so the deferred cleanup runs before
	// os.Exit (which skips defers). This guarantees cluster shutdown
	// even if a test panics.
	exit := func() int {
		defer t.runCleanups()
		return m.Run()
	}()
	os.Exit(exit)
}

// mainT is a minimal *testing.T-shaped value usable from TestMain. It
// satisfies the subset of methods that clustertest.Start invokes:
// Helper (no-op), Cleanup (recorded for end-of-suite execution),
// Fatalf (panics to abort TestMain), Errorf (logs to stderr).
type mainT struct {
	mu       sync.Mutex
	cleanups []func()
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

// Errorf surfaces non-fatal warnings to stderr instead of dropping them.
// clustertest.Start does not currently call Errorf, but adding the method
// to the TB interface in the future would silently mask diagnostics
// without this implementation.
func (m *mainT) Errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[clustertest] "+format+"\n", args...)
}

func (m *mainT) runCleanups() {
	// Snapshot under the lock and release before invoking so a cleanup
	// can call Cleanup itself without re-entering the mutex.
	m.mu.Lock()
	cleanups := m.cleanups
	m.cleanups = nil
	m.mu.Unlock()

	// LIFO order, like *testing.T. Each cleanup is wrapped in its own
	// recover so a panicking cleanup does not prevent later cleanups
	// (notably the cluster shutdown) from running.
	for i := len(cleanups) - 1; i >= 0; i-- {
		func(fn func()) {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "[clustertest] cleanup panicked: %v\n", r)
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

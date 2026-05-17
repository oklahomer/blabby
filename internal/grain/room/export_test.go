package room

import (
	"time"

	"github.com/oklahomer/blabby/internal/id"
)

// Test-only seams. export_test.go is compiled only during `go test` so the
// production binary surface stays clean while tests can inspect private state.

// Members exposes a sorted snapshot of the current member set.
func (g *Grain) Members() []id.UserID { return g.state.memberIDs() }

// RecentMessageCount returns the size of the recent-message buffer.
func (g *Grain) RecentMessageCount() int { return len(g.state.recentMessages) }

// SetNotifier injects a userNotifier for tests; production code uses NewKind.
func (g *Grain) SetNotifier(n userNotifier) { g.notifier = n }

// SetClock injects the timestamp source for tests.
func (g *Grain) SetClock(now func() time.Time) { g.now = now }

// UserNotifier is the test-visible alias of the unexported userNotifier
// interface so test fakes outside this file can declare conformance.
type UserNotifier = userNotifier

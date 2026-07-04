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

// SetLoader injects a RoomLoader for tests; production code uses NewKind. Call
// before Init so activation hydrates from the stub.
func (g *Grain) SetLoader(l RoomLoader) { g.loader = l }

// SetMembershipStore injects a MembershipStore for tests; production wires it via
// NewKind's WithMembership option. Call before Init so activation seeds the
// member cache from the stub.
func (g *Grain) SetMembershipStore(s MembershipStore) { g.membership = s }

// SetMessageStore injects a MessageStore for tests; production wires it via
// NewKind's WithMessages option.
func (g *Grain) SetMessageStore(s MessageStore) { g.messages = s }

// SetNotifier injects a userNotifier for tests; production code uses NewKind.
func (g *Grain) SetNotifier(n userNotifier) { g.notifier = n }

// SetClock injects the timestamp source for tests.
func (g *Grain) SetClock(now func() time.Time) { g.now = now }

// UseSyncFanout installs an inline (synchronous) fan-out dispatcher backed by
// the grain's current notifier, so unit tests observe fan-out deterministically
// and Init does not spawn the production fan-out child actor. Call after
// SetNotifier and before Init.
func (g *Grain) UseSyncFanout() { g.fanout = &syncDispatcher{notifier: g.notifier} }

// UserNotifier is the test-visible alias of the unexported userNotifier
// interface so test fakes outside this file can declare conformance.
type UserNotifier = userNotifier

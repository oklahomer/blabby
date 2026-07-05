package room

import (
	"reflect"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/id"
)

func mustUserID(t *testing.T, raw string) id.UserID {
	t.Helper()
	u, err := id.ParseUserID(raw)
	if err != nil {
		t.Fatalf("mustUserID(%q): %v", raw, err)
	}
	return u
}

// mustUserRef builds a typed id.UserRef from a raw id and display name,
// failing the test on any construction error. It keeps the member-cache
// tests readable now that addMember/refreshMember take a UserRef.
func mustUserRef(t *testing.T, rawID, name string) id.UserRef {
	t.Helper()
	code, err := id.NewPublicCode()
	if err != nil {
		t.Fatalf("NewPublicCode: %v", err)
	}
	ref, err := id.NewUserRef(mustUserID(t, rawID), code, name)
	if err != nil {
		t.Fatalf("mustUserRef(%q, %q): %v", rawID, name, err)
	}
	return ref
}

func TestRoomState_AddRemoveMember(t *testing.T) {
	alice := mustUserID(t, "1")
	bob := mustUserID(t, "2")
	aliceRef := mustUserRef(t, "1", "1")
	bobRef := mustUserRef(t, "2", "2")

	tests := []struct {
		name      string
		setup     func(s *roomState)
		op        func(s *roomState) bool
		wantOK    bool
		wantState []id.UserID
	}{
		{
			name:      "add new member returns true and stores id",
			op:        func(s *roomState) bool { return s.addMember(aliceRef) },
			wantOK:    true,
			wantState: []id.UserID{alice},
		},
		{
			name:      "add duplicate member returns false and keeps state",
			setup:     func(s *roomState) { s.addMember(aliceRef) },
			op:        func(s *roomState) bool { return s.addMember(aliceRef) },
			wantOK:    false,
			wantState: []id.UserID{alice},
		},
		{
			name:      "remove existing member returns true and clears state",
			setup:     func(s *roomState) { s.addMember(aliceRef) },
			op:        func(s *roomState) bool { return s.removeMember(alice) },
			wantOK:    true,
			wantState: nil,
		},
		{
			name:      "remove non-member returns false and keeps state",
			setup:     func(s *roomState) { s.addMember(bobRef) },
			op:        func(s *roomState) bool { return s.removeMember(alice) },
			wantOK:    false,
			wantState: []id.UserID{bob},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newRoomState()
			if tt.setup != nil {
				tt.setup(&s)
			}
			if got := tt.op(&s); got != tt.wantOK {
				t.Errorf("op result: got %v, want %v", got, tt.wantOK)
			}
			got := s.memberIDs()
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, tt.wantState) {
				t.Errorf("members: got %v, want %v", got, tt.wantState)
			}
		})
	}
}

func TestRoomState_IsMember(t *testing.T) {
	alice := mustUserID(t, "1")
	bob := mustUserID(t, "2")
	s := newRoomState()
	s.addMember(mustUserRef(t, "1", "1"))

	if !s.isMember(alice) {
		t.Errorf("isMember(alice): got false, want true")
	}
	if s.isMember(bob) {
		t.Errorf("isMember(bob): got true, want false")
	}
}

func TestRoomState_MemberRef_CachesNameAndRefresh(t *testing.T) {
	alice := mustUserID(t, "1")
	bob := mustUserID(t, "2")
	s := newRoomState()
	s.addMember(mustUserRef(t, "1", "Alice"))

	ref, ok := s.memberRef(alice)
	if !ok {
		t.Fatalf("memberRef(alice): got ok=false, want true")
	}
	if ref.ID() != alice {
		t.Errorf("memberRef(alice).ID(): got %v, want %v", ref.ID(), alice)
	}
	if ref.Name() != "Alice" {
		t.Errorf("memberRef(alice).Name(): got %q, want %q", ref.Name(), "Alice")
	}

	// refreshMember updates an existing member's cached display name.
	s.refreshMember(mustUserRef(t, "1", "Alice Renamed"))
	ref, _ = s.memberRef(alice)
	if ref.Name() != "Alice Renamed" {
		t.Errorf("after refresh, memberRef(alice).Name(): got %q, want %q", ref.Name(), "Alice Renamed")
	}

	// refreshMember is a no-op for a non-member and must not resurrect one.
	s.refreshMember(mustUserRef(t, "2", "Bob"))
	if _, ok := s.memberRef(bob); ok {
		t.Errorf("memberRef(bob): got ok=true, want false (refresh must not add a non-member)")
	}
}

func TestRoomState_MemberIDs_Sorted(t *testing.T) {
	s := newRoomState()
	for _, ref := range []id.UserRef{mustUserRef(t, "3", "3"), mustUserRef(t, "1", "1"), mustUserRef(t, "2", "2")} {
		s.addMember(ref)
	}

	got := s.memberIDs()
	want := []id.UserID{mustUserID(t, "1"), mustUserID(t, "2"), mustUserID(t, "3")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("memberIDs: got %v, want %v (must be sorted)", got, want)
	}

	// Mutating the snapshot must not affect the underlying state.
	got[0] = mustUserID(t, "777")
	if !s.isMember(mustUserID(t, "1")) {
		t.Errorf("snapshot mutation leaked into state")
	}
}

func TestRoomState_RecordMessage_RingBufferBound(t *testing.T) {
	s := newRoomState()
	s.maxRecentMessages = 3
	alice := mustUserID(t, "1")

	for i := int64(1); i <= 5; i++ {
		s.recordMessage(chatMessage{senderID: alice, text: "msg", timestamp: time.UnixMilli(i)})
	}

	if got := len(s.recentMessages); got != 3 {
		t.Fatalf("len(recentMessages): got %d, want 3", got)
	}

	// Expect the three most-recent messages (timestamps 3, 4, 5) preserved
	// in arrival order; oldest two evicted.
	wantTS := []int64{3, 4, 5}
	for i, msg := range s.recentMessages {
		if got := msg.timestamp.UnixMilli(); got != wantTS[i] {
			t.Errorf("recentMessages[%d].timestamp.UnixMilli(): got %d, want %d", i, got, wantTS[i])
		}
	}
}

func TestRoomState_RecordMessage_BelowBound(t *testing.T) {
	s := newRoomState()
	s.maxRecentMessages = 10

	for i := int64(1); i <= 4; i++ {
		s.recordMessage(chatMessage{timestamp: time.UnixMilli(i)})
	}

	if got := len(s.recentMessages); got != 4 {
		t.Errorf("len(recentMessages): got %d, want 4", got)
	}
}

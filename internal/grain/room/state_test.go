package room

import (
	"reflect"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/id"
)

func mustUserID(t *testing.T, raw string) id.UserID {
	t.Helper()
	u, err := id.NewUserID(raw)
	if err != nil {
		t.Fatalf("mustUserID(%q): %v", raw, err)
	}
	return u
}

func TestRoomState_AddRemoveMember(t *testing.T) {
	alice := mustUserID(t, "alice")
	bob := mustUserID(t, "bob")

	tests := []struct {
		name      string
		setup     func(s *roomState)
		op        func(s *roomState) bool
		wantOK    bool
		wantState []id.UserID
	}{
		{
			name:      "add new member returns true and stores id",
			op:        func(s *roomState) bool { return s.addMember(alice) },
			wantOK:    true,
			wantState: []id.UserID{alice},
		},
		{
			name:      "add duplicate member returns false and keeps state",
			setup:     func(s *roomState) { s.addMember(alice) },
			op:        func(s *roomState) bool { return s.addMember(alice) },
			wantOK:    false,
			wantState: []id.UserID{alice},
		},
		{
			name:      "remove existing member returns true and clears state",
			setup:     func(s *roomState) { s.addMember(alice) },
			op:        func(s *roomState) bool { return s.removeMember(alice) },
			wantOK:    true,
			wantState: nil,
		},
		{
			name:      "remove non-member returns false and keeps state",
			setup:     func(s *roomState) { s.addMember(bob) },
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
	alice := mustUserID(t, "alice")
	bob := mustUserID(t, "bob")
	s := newRoomState()
	s.addMember(alice)

	if !s.isMember(alice) {
		t.Errorf("isMember(alice): got false, want true")
	}
	if s.isMember(bob) {
		t.Errorf("isMember(bob): got true, want false")
	}
}

func TestRoomState_MemberIDs_Sorted(t *testing.T) {
	s := newRoomState()
	for _, userID := range []id.UserID{mustUserID(t, "charlie"), mustUserID(t, "alice"), mustUserID(t, "bob")} {
		s.addMember(userID)
	}

	got := s.memberIDs()
	want := []id.UserID{mustUserID(t, "alice"), mustUserID(t, "bob"), mustUserID(t, "charlie")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("memberIDs: got %v, want %v (must be sorted)", got, want)
	}

	// Mutating the snapshot must not affect the underlying state.
	got[0] = mustUserID(t, "mutated")
	if !s.isMember(mustUserID(t, "alice")) {
		t.Errorf("snapshot mutation leaked into state")
	}
}

func TestRoomState_RecordMessage_RingBufferBound(t *testing.T) {
	s := newRoomState()
	s.maxRecentMessages = 3
	alice := mustUserID(t, "alice")

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

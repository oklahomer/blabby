package user

import (
	"reflect"
	"testing"

	"github.com/asynkron/protoactor-go/actor"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

func mustRoomID(t *testing.T, raw string) id.RoomID {
	t.Helper()
	r, err := id.ParseRoomID(raw)
	if err != nil {
		t.Fatalf("mustRoomID(%q): %v", raw, err)
	}
	return r
}

// joinRoomID records a minimal active ref for raw in s, for membership-set tests
// that care only about which rooms are joined, not the cached metadata.
func joinRoomID(t *testing.T, s *userState, raw string) {
	t.Helper()
	rid := mustRoomID(t, raw)
	s.joinRoom(domain.RoomRef{ID: rid, Status: domain.RoomStatusActive})
}

func TestUserState_AddConnection(t *testing.T) {
	pid1 := actor.NewPID("addr", "id-1")
	pid2 := actor.NewPID("addr", "id-2")

	tests := []struct {
		name    string
		seed    []*actor.PID
		add     *actor.PID
		wantPID []*actor.PID
	}{
		{
			name:    "add to empty",
			seed:    nil,
			add:     pid1,
			wantPID: []*actor.PID{pid1},
		},
		{
			name:    "add second distinct connection",
			seed:    []*actor.PID{pid1},
			add:     pid2,
			wantPID: []*actor.PID{pid1, pid2},
		},
		{
			name:    "re-add same PID is a no-op",
			seed:    []*actor.PID{pid1},
			add:     pid1,
			wantPID: []*actor.PID{pid1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newUserState()
			for _, p := range tc.seed {
				s.addConnection(p)
			}

			s.addConnection(tc.add)

			if got := s.connectionPIDs(); !reflect.DeepEqual(got, tc.wantPID) {
				t.Errorf("connectionPIDs: got %v, want %v", got, tc.wantPID)
			}
		})
	}
}

func TestUserState_RemoveConnection(t *testing.T) {
	pid1 := actor.NewPID("addr", "id-1")
	pid2 := actor.NewPID("addr", "id-2")
	missing := actor.NewPID("addr", "id-missing")

	tests := []struct {
		name    string
		seed    []*actor.PID
		remove  *actor.PID
		wantPID []*actor.PID
	}{
		{
			name:    "remove known PID",
			seed:    []*actor.PID{pid1, pid2},
			remove:  pid1,
			wantPID: []*actor.PID{pid2},
		},
		{
			name:    "remove unknown PID is a no-op",
			seed:    []*actor.PID{pid1},
			remove:  missing,
			wantPID: []*actor.PID{pid1},
		},
		{
			name:    "remove from empty set is a no-op",
			seed:    nil,
			remove:  missing,
			wantPID: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newUserState()
			for _, p := range tc.seed {
				s.addConnection(p)
			}

			s.removeConnection(tc.remove)

			got := s.connectionPIDs()
			if !reflect.DeepEqual(got, tc.wantPID) {
				t.Errorf("connectionPIDs: got %v, want %v", got, tc.wantPID)
			}
		})
	}
}

func TestUserState_ConnectionPIDs_DeterministicOrder(t *testing.T) {
	s := newUserState()
	pidB := actor.NewPID("addr", "B")
	pidA := actor.NewPID("addr", "A")
	pidC := actor.NewPID("addr", "C")
	// Insert out of order to prove sort, not insertion-order, drives output.
	s.addConnection(pidB)
	s.addConnection(pidA)
	s.addConnection(pidC)

	got := s.connectionPIDs()
	want := []*actor.PID{pidA, pidB, pidC}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("connectionPIDs: got %v, want %v", got, want)
	}
}

func TestUserState_ConnectionPIDs_EmptyReturnsNil(t *testing.T) {
	s := newUserState()
	if got := s.connectionPIDs(); got != nil {
		t.Errorf("connectionPIDs on empty: got %v, want nil", got)
	}
}

func TestUserState_JoinedRooms(t *testing.T) {
	t.Run("join then snapshot is sorted", func(t *testing.T) {
		s := newUserState()
		joinRoomID(t, &s, "22")
		joinRoomID(t, &s, "20")
		joinRoomID(t, &s, "21")

		got := s.joinedRoomIDs()
		want := []id.RoomID{mustRoomID(t, "20"), mustRoomID(t, "21"), mustRoomID(t, "22")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("joinedRoomIDs: got %v, want %v", got, want)
		}
	})

	t.Run("re-join is a no-op", func(t *testing.T) {
		s := newUserState()
		joinRoomID(t, &s, "4")
		joinRoomID(t, &s, "4")

		got := s.joinedRoomIDs()
		want := []id.RoomID{mustRoomID(t, "4")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("joinedRoomIDs: got %v, want %v", got, want)
		}
	})

	t.Run("leave removes membership", func(t *testing.T) {
		s := newUserState()
		joinRoomID(t, &s, "4")
		joinRoomID(t, &s, "5")
		s.leaveRoom(mustRoomID(t, "4"))

		got := s.joinedRoomIDs()
		want := []id.RoomID{mustRoomID(t, "5")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("joinedRoomIDs: got %v, want %v", got, want)
		}
	})

	t.Run("leave unknown room is a no-op", func(t *testing.T) {
		s := newUserState()
		joinRoomID(t, &s, "4")
		s.leaveRoom(mustRoomID(t, "99"))

		got := s.joinedRoomIDs()
		want := []id.RoomID{mustRoomID(t, "4")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("joinedRoomIDs: got %v, want %v", got, want)
		}
	})

	t.Run("empty snapshot", func(t *testing.T) {
		s := newUserState()
		got := s.joinedRoomIDs()
		if len(got) != 0 {
			t.Errorf("joinedRoomIDs on empty: got %v, want empty", got)
		}
	})
}

func TestUserState_JoinedRoomRefs(t *testing.T) {
	t.Run("returns cached refs sorted by room id", func(t *testing.T) {
		s := newUserState()
		s.joinRoom(domain.RoomRef{ID: mustRoomID(t, "22"), Name: "Room 22", Status: domain.RoomStatusActive})
		s.joinRoom(domain.RoomRef{ID: mustRoomID(t, "20"), Name: "Room 20", Status: domain.RoomStatusActive})

		got := s.joinedRoomRefs()
		want := []domain.RoomRef{
			{ID: mustRoomID(t, "20"), Name: "Room 20", Status: domain.RoomStatusActive},
			{ID: mustRoomID(t, "22"), Name: "Room 22", Status: domain.RoomStatusActive},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("joinedRoomRefs: got %v, want %v", got, want)
		}
	})

	t.Run("re-join refreshes the cached ref", func(t *testing.T) {
		s := newUserState()
		s.joinRoom(domain.RoomRef{ID: mustRoomID(t, "4"), Name: "Old", Status: domain.RoomStatusActive})
		s.joinRoom(domain.RoomRef{ID: mustRoomID(t, "4"), Name: "New", Status: domain.RoomStatusActive})

		got := s.joinedRoomRefs()
		if len(got) != 1 || got[0].Name != "New" {
			t.Errorf("joinedRoomRefs after re-join: got %v, want a single ref named New", got)
		}
	})

	t.Run("empty snapshot", func(t *testing.T) {
		s := newUserState()
		if got := s.joinedRoomRefs(); len(got) != 0 {
			t.Errorf("joinedRoomRefs on empty: got %v, want empty", got)
		}
	})
}

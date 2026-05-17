package user

import (
	"reflect"
	"testing"

	"github.com/asynkron/protoactor-go/actor"

	"github.com/oklahomer/blabby/internal/ids"
)

func mustRoomID(t *testing.T, raw string) ids.RoomID {
	t.Helper()
	r, err := ids.NewRoomID(raw)
	if err != nil {
		t.Fatalf("mustRoomID(%q): %v", raw, err)
	}
	return r
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
		s.joinRoom(mustRoomID(t, "zulu"))
		s.joinRoom(mustRoomID(t, "alpha"))
		s.joinRoom(mustRoomID(t, "mike"))

		got := s.joinedRoomIDs()
		want := []ids.RoomID{mustRoomID(t, "alpha"), mustRoomID(t, "mike"), mustRoomID(t, "zulu")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("joinedRoomIDs: got %v, want %v", got, want)
		}
	})

	t.Run("re-join is a no-op", func(t *testing.T) {
		s := newUserState()
		s.joinRoom(mustRoomID(t, "general"))
		s.joinRoom(mustRoomID(t, "general"))

		got := s.joinedRoomIDs()
		want := []ids.RoomID{mustRoomID(t, "general")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("joinedRoomIDs: got %v, want %v", got, want)
		}
	})

	t.Run("leave removes membership", func(t *testing.T) {
		s := newUserState()
		s.joinRoom(mustRoomID(t, "general"))
		s.joinRoom(mustRoomID(t, "random"))
		s.leaveRoom(mustRoomID(t, "general"))

		got := s.joinedRoomIDs()
		want := []ids.RoomID{mustRoomID(t, "random")}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("joinedRoomIDs: got %v, want %v", got, want)
		}
	})

	t.Run("leave unknown room is a no-op", func(t *testing.T) {
		s := newUserState()
		s.joinRoom(mustRoomID(t, "general"))
		s.leaveRoom(mustRoomID(t, "missing"))

		got := s.joinedRoomIDs()
		want := []ids.RoomID{mustRoomID(t, "general")}
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

package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// fakeCreationRooms returns one queued result per Create call, so a collision
// followed by a success can be scripted.
type fakeCreationRooms struct {
	results []roomResult
	calls   int
	last    persistence.RoomCreateParams
}

type roomResult struct {
	room persistence.Room
	err  error
}

func (f *fakeCreationRooms) Create(_ context.Context, _ postgres.Querier, params persistence.RoomCreateParams) (persistence.Room, error) {
	if f.calls >= len(f.results) {
		return persistence.Room{}, errors.New("unexpected Create")
	}
	f.last = params
	result := f.results[f.calls]
	f.calls++
	return result.room, result.err
}

type fakeCreationUsers struct {
	user persistence.User
	err  error
}

func (f *fakeCreationUsers) FindByID(context.Context, postgres.Querier, id.UserID) (persistence.User, error) {
	return f.user, f.err
}

type fakeCreationMemberships struct {
	err   error
	calls int
	room  id.RoomID
	ref   domain.UserRef
	role  domain.MembershipRole
}

func (f *fakeCreationMemberships) Add(_ context.Context, _ postgres.Querier, roomID id.RoomID, ref domain.UserRef, role domain.MembershipRole) error {
	f.calls++
	f.room, f.ref, f.role = roomID, ref, role
	return f.err
}

type fakeCreationJournal struct {
	err   error
	calls int
	room  id.RoomID
	ref   domain.UserRef
	kind  persistence.MemberEventKind
}

func (f *fakeCreationJournal) AppendMembership(_ context.Context, _ postgres.Querier, roomID id.RoomID, actor domain.UserRef, kind persistence.MemberEventKind) (id.EventID, time.Time, error) {
	f.calls++
	f.room, f.ref, f.kind = roomID, actor, kind
	if f.err != nil {
		return id.EventID{}, time.Time{}, f.err
	}
	eid, err := id.NewEventID(555)
	if err != nil {
		return id.EventID{}, time.Time{}, err
	}
	return eid, time.Unix(1000, 0), nil
}

func creationUser(t *testing.T, displayName string) persistence.User {
	t.Helper()
	uid, err := id.NewUserID(1)
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	code, err := id.ParsePublicCode("A000000001")
	if err != nil {
		t.Fatalf("ParsePublicCode: %v", err)
	}
	return persistence.User{ID: uid, PublicCode: code, DisplayName: displayName}
}

func createdRoom(t *testing.T, rawID int64, code, name string) persistence.Room {
	t.Helper()
	rid, err := id.NewRoomID(rawID)
	if err != nil {
		t.Fatalf("NewRoomID: %v", err)
	}
	pc, err := id.ParsePublicCode(code)
	if err != nil {
		t.Fatalf("ParsePublicCode: %v", err)
	}
	return persistence.Room{ID: rid, PublicCode: pc, DisplayName: name, Status: domain.RoomStatusActive}
}

func creationName(t *testing.T, raw string) domain.RoomName {
	t.Helper()
	name, err := domain.NewRoomName(raw)
	if err != nil {
		t.Fatalf("NewRoomName: %v", err)
	}
	return name
}

func newCreationService(rooms *fakeCreationRooms, users *fakeCreationUsers, members *fakeCreationMemberships, jrnl *fakeCreationJournal) (*RoomCreationService, *fakeRegistrationTx) {
	tx := &fakeRegistrationTx{}
	return NewRoomCreationService(rooms, users, members, jrnl, tx), tx
}

func TestCreateRoom_CreatesOwnerAndFoundingEvent(t *testing.T) {
	actor := mustUserID(t, "1")
	rooms := &fakeCreationRooms{results: []roomResult{{room: createdRoom(t, 42, "K000000042", "Standup")}}}
	users := &fakeCreationUsers{user: creationUser(t, "alice")}
	members := &fakeCreationMemberships{}
	jrnl := &fakeCreationJournal{}
	svc, tx := newCreationService(rooms, users, members, jrnl)

	info, err := svc.CreateRoom(context.Background(), actor, creationName(t, "Standup"))
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if info.Name != "Standup" || info.PublicID() != "RK000000042" {
		t.Errorf("info = %+v, want Standup / RK000000042", info)
	}
	if rooms.last.Name.String() != "Standup" || rooms.last.CreatedBy != actor {
		t.Errorf("CreateParams = %+v, want Standup by actor", rooms.last)
	}
	if members.calls != 1 || members.role != domain.MembershipRoleOwner || members.ref.Name() != "alice" || members.room.Int64() != 42 {
		t.Errorf("membership Add = %+v role=%s ref=%s, want owner alice in room 42", members.room, members.role, members.ref.Name())
	}
	if jrnl.calls != 1 || jrnl.kind != persistence.MemberJoined || jrnl.room.Int64() != 42 {
		t.Errorf("journal append = room %v kind %v, want member_joined in room 42", jrnl.room, jrnl.kind)
	}
	if tx.commits != 1 {
		t.Errorf("commits = %d, want 1", tx.commits)
	}
}

func TestCreateRoom_RetriesPublicCodeCollision(t *testing.T) {
	actor := mustUserID(t, "1")
	rooms := &fakeCreationRooms{results: []roomResult{
		{err: persistence.ErrRoomPublicCodeCollision},
		{room: createdRoom(t, 42, "K000000042", "Standup")},
	}}
	svc, tx := newCreationService(rooms, &fakeCreationUsers{user: creationUser(t, "alice")}, &fakeCreationMemberships{}, &fakeCreationJournal{})

	if _, err := svc.CreateRoom(context.Background(), actor, creationName(t, "Standup")); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if rooms.calls != 2 {
		t.Errorf("Create calls = %d, want 2 (collision then success)", rooms.calls)
	}
	if tx.calls != 2 || tx.commits != 1 {
		t.Errorf("tx calls/commits = %d/%d, want 2/1", tx.calls, tx.commits)
	}
}

func TestCreateRoom_ExhaustsCollisionRetries(t *testing.T) {
	results := make([]roomResult, roomCreateCollisionRetryLimit+1)
	for i := range results {
		results[i] = roomResult{err: persistence.ErrRoomPublicCodeCollision}
	}
	svc, _ := newCreationService(&fakeCreationRooms{results: results}, &fakeCreationUsers{user: creationUser(t, "alice")}, &fakeCreationMemberships{}, &fakeCreationJournal{})

	_, err := svc.CreateRoom(context.Background(), mustUserID(t, "1"), creationName(t, "Standup"))
	if err == nil || !errors.Is(err, persistence.ErrRoomPublicCodeCollision) {
		t.Fatalf("CreateRoom = %v, want exhausted-collision error wrapping the sentinel", err)
	}
}

func TestCreateRoom_FailuresRollBack(t *testing.T) {
	tests := []struct {
		name    string
		users   *fakeCreationUsers
		members *fakeCreationMemberships
		jrnl    *fakeCreationJournal
	}{
		{name: "creator lookup fails", users: &fakeCreationUsers{err: persistence.ErrUserNotFound}, members: &fakeCreationMemberships{}, jrnl: &fakeCreationJournal{}},
		{name: "owner membership write fails", users: &fakeCreationUsers{user: creationUser(t, "alice")}, members: &fakeCreationMemberships{err: errors.New("insert failed")}, jrnl: &fakeCreationJournal{}},
		{name: "founding event append fails", users: &fakeCreationUsers{user: creationUser(t, "alice")}, members: &fakeCreationMemberships{}, jrnl: &fakeCreationJournal{err: errors.New("append failed")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rooms := &fakeCreationRooms{results: []roomResult{{room: createdRoom(t, 42, "K000000042", "Standup")}}}
			svc, tx := newCreationService(rooms, tc.users, tc.members, tc.jrnl)

			if _, err := svc.CreateRoom(context.Background(), mustUserID(t, "1"), creationName(t, "Standup")); err == nil {
				t.Fatal("CreateRoom: want an error")
			}
			if tx.commits != 0 {
				t.Errorf("commits = %d, want 0 (the transaction must roll back)", tx.commits)
			}
		})
	}
}

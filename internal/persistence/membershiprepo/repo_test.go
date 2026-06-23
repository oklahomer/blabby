package membershiprepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// fakeQuerier is an in-memory postgres.Querier for exercising the repo's control
// flow without a database. exec drives Add/Remove; query drives the list reads.
type fakeQuerier struct {
	exec  func(sql string, args ...any) (pgconn.CommandTag, error)
	query func(sql string, args ...any) (pgx.Rows, error)
}

var _ postgres.Querier = (*fakeQuerier)(nil)

func (f *fakeQuerier) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return f.exec(sql, args...)
}

func (f *fakeQuerier) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	return f.query(sql, args...)
}

func (f *fakeQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("membershiprepo does not use QueryRow")
}

// fakeRows replays a fixed set of rows (each a slice of column values in scan
// order) through the pgx.Rows contract the collect helpers depend on.
type fakeRows struct {
	rows [][]any
	idx  int
	err  error
}

func (f *fakeRows) Close()                                       {}
func (f *fakeRows) Err() error                                   { return f.err }
func (f *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (f *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (f *fakeRows) Values() ([]any, error)                       { return nil, nil }
func (f *fakeRows) RawValues() [][]byte                          { return nil }
func (f *fakeRows) Conn() *pgx.Conn                              { return nil }

func (f *fakeRows) Next() bool {
	if f.idx >= len(f.rows) {
		return false
	}
	f.idx++
	return true
}

func (f *fakeRows) Scan(dest ...any) error { return assignAll(dest, f.rows[f.idx-1]) }

// assignAll copies column values into the Scan destinations, matching the types
// the row structs pass (*int64, *string, *time.Time).
func assignAll(dest []any, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("fake scan: %d destinations, %d values", len(dest), len(values))
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *int64:
			*d = values[i].(int64)
		case *string:
			*d = values[i].(string)
		case *time.Time:
			*d = values[i].(time.Time)
		default:
			return fmt.Errorf("fake scan: unsupported destination %T", dest[i])
		}
	}
	return nil
}

func mustUserRef(t *testing.T, rawID int64, name string) id.UserRef {
	t.Helper()
	uid, err := id.NewUserID(rawID)
	if err != nil {
		t.Fatalf("NewUserID(%d): %v", rawID, err)
	}
	ref, err := id.NewUserRef(uid, name)
	if err != nil {
		t.Fatalf("NewUserRef(%d,%q): %v", rawID, name, err)
	}
	return ref
}

func mustRoomID(t *testing.T, v int64) id.RoomID {
	t.Helper()
	rid, err := id.NewRoomID(v)
	if err != nil {
		t.Fatalf("NewRoomID(%d): %v", v, err)
	}
	return rid
}

func mustUserID(t *testing.T, v int64) id.UserID {
	t.Helper()
	uid, err := id.NewUserID(v)
	if err != nil {
		t.Fatalf("NewUserID(%d): %v", v, err)
	}
	return uid
}

func TestListByRoom(t *testing.T) {
	ts := time.Unix(100, 0).UTC()
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		gotSQL, gotArgs = sql, args
		return &fakeRows{rows: [][]any{
			{int64(1), "alice", "owner", ts},
			{int64(2), "bob", "member", ts},
		}}, nil
	}}

	members, err := New().ListByRoom(context.Background(), fq, mustRoomID(t, 4))
	if err != nil {
		t.Fatalf("ListByRoom: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members: got %d, want 2", len(members))
	}
	if members[0].User.ID().Int64() != 1 || members[0].User.Name() != "alice" || members[0].Role != domain.MembershipRoleOwner {
		t.Errorf("members[0] = %+v", members[0])
	}
	if members[1].Role != domain.MembershipRoleMember {
		t.Errorf("members[1].Role = %q, want member", members[1].Role)
	}
	if gotArgs[0].(int64) != 4 {
		t.Errorf("room id arg = %v, want 4", gotArgs[0])
	}
	// The role is read as text so it scans into a Go string.
	if !strings.Contains(gotSQL, "m.role::text") {
		t.Errorf("ListByRoom SQL must read role as text: %s", gotSQL)
	}
}

func TestListByRoom_RowError(t *testing.T) {
	// A non-positive id in a row is a data-integrity error surfaced, not trusted.
	fq := &fakeQuerier{query: func(string, ...any) (pgx.Rows, error) {
		return &fakeRows{rows: [][]any{{int64(0), "ghost", "member", time.Unix(0, 0)}}}, nil
	}}
	if _, err := New().ListByRoom(context.Background(), fq, mustRoomID(t, 4)); err == nil {
		t.Fatal("ListByRoom: want an error for a non-positive user id row")
	}
}

func TestListByUser(t *testing.T) {
	ts := time.Unix(1700000000, 123456000).UTC()
	var gotSQL string
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		gotSQL = sql
		return &fakeRows{rows: [][]any{
			{int64(4), "G000000004", "General", "active", ts},
		}}, nil
	}}

	rooms, err := New().ListByUser(context.Background(), fq, mustUserID(t, 1))
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(rooms) != 1 {
		t.Fatalf("rooms: got %d, want 1", len(rooms))
	}
	r := rooms[0]
	if r.ID.Int64() != 4 || r.PublicCode.String() != "G000000004" || r.Name != "General" || r.Status != domain.RoomStatusActive {
		t.Errorf("room = %+v", r)
	}
	if r.MetadataVersion != ts.UnixMicro() {
		t.Errorf("MetadataVersion = %d, want %d (updated_at micros)", r.MetadataVersion, ts.UnixMicro())
	}
	// Joined-rooms are active-only, mirroring roomrepo's reads.
	if !strings.Contains(gotSQL, "r.status = 'active'") {
		t.Errorf("ListByUser SQL must filter active rooms: %s", gotSQL)
	}
}

func TestAdd(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{exec: func(sql string, args ...any) (pgconn.CommandTag, error) {
		gotSQL, gotArgs = sql, args
		return pgconn.CommandTag{}, nil
	}}

	err := New().Add(context.Background(), fq, mustRoomID(t, 4), mustUserRef(t, 2, "bob"), domain.MembershipRoleMember)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if gotArgs[0].(int64) != 4 || gotArgs[1].(int64) != 2 || gotArgs[2].(string) != "member" {
		t.Errorf("add args = %v, want [4 2 member]", gotArgs)
	}
	// The role param is cast to the enum type explicitly.
	if !strings.Contains(gotSQL, "$3::membership_role") {
		t.Errorf("Add SQL must cast the role param to the enum: %s", gotSQL)
	}
}

func TestAdd_PropagatesError(t *testing.T) {
	sentinel := errors.New("duplicate key")
	fq := &fakeQuerier{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, sentinel
	}}
	err := New().Add(context.Background(), fq, mustRoomID(t, 4), mustUserRef(t, 2, "bob"), domain.MembershipRoleMember)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Add: got %v, want the wrapped db error", err)
	}
}

func TestRemove(t *testing.T) {
	var gotArgs []any
	fq := &fakeQuerier{exec: func(_ string, args ...any) (pgconn.CommandTag, error) {
		gotArgs = args
		return pgconn.NewCommandTag("DELETE 1"), nil
	}}

	if err := New().Remove(context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 2)); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if gotArgs[0].(int64) != 4 || gotArgs[1].(int64) != 2 {
		t.Errorf("remove args = %v, want [4 2]", gotArgs)
	}
}

func TestRemove_NotFoundIsStrict(t *testing.T) {
	// A 0-row delete is a cache/DB divergence, surfaced so the caller fails closed
	// rather than appending a member_left event for a change that did not happen.
	fq := &fakeQuerier{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("DELETE 0"), nil
	}}
	err := New().Remove(context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 2))
	if !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("Remove(absent): got %v, want ErrMembershipNotFound", err)
	}
}

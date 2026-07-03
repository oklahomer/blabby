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
// flow without a database. exec drives Add/UpdateRole/TransferOwnership; query
// drives the list reads; queryRow drives Remove and GetRole.
type fakeQuerier struct {
	exec     func(sql string, args ...any) (pgconn.CommandTag, error)
	query    func(sql string, args ...any) (pgx.Rows, error)
	queryRow func(sql string, args ...any) pgx.Row
}

var _ postgres.Querier = (*fakeQuerier)(nil)

func (f *fakeQuerier) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return f.exec(sql, args...)
}

func (f *fakeQuerier) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	return f.query(sql, args...)
}

func (f *fakeQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if f.queryRow == nil {
		panic("fakeQuerier: QueryRow not stubbed")
	}
	return f.queryRow(sql, args...)
}

// fakeRow replays one row (column values in scan order) or an error.
type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return assignAll(dest, r.values)
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
		case *bool:
			*d = values[i].(bool)
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
	// The guarded delete reports (existed, deleted) in one statement; each pair
	// maps to a distinct outcome.
	tests := []struct {
		name    string
		existed bool
		deleted bool
		wantErr error
	}{
		{name: "member removed", existed: true, deleted: true, wantErr: nil},
		{name: "owner is refused", existed: true, deleted: false, wantErr: ErrOwnerCannotLeave},
		// A missing row is a cache/DB divergence, surfaced so the caller fails
		// closed rather than appending a member_left event for a change that did
		// not happen.
		{name: "absent row is strict", existed: false, deleted: false, wantErr: ErrMembershipNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotSQL string
			var gotArgs []any
			fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
				gotSQL, gotArgs = sql, args
				return fakeRow{values: []any{tc.existed, tc.deleted}}
			}}

			err := New().Remove(context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 2))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Remove: got %v, want %v", err, tc.wantErr)
			}
			if gotArgs[0].(int64) != 4 || gotArgs[1].(int64) != 2 {
				t.Errorf("remove args = %v, want [4 2]", gotArgs)
			}
			if !strings.Contains(gotSQL, "role <> 'owner'") {
				t.Errorf("Remove SQL must exclude the owner from the delete: %s", gotSQL)
			}
		})
	}
}

func TestGetRole(t *testing.T) {
	t.Run("member role", func(t *testing.T) {
		var gotArgs []any
		fq := &fakeQuerier{queryRow: func(_ string, args ...any) pgx.Row {
			gotArgs = args
			return fakeRow{values: []any{"admin"}}
		}}
		role, err := New().GetRole(context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 2))
		if err != nil {
			t.Fatalf("GetRole: %v", err)
		}
		if role != domain.MembershipRoleAdmin {
			t.Errorf("role = %q, want admin", role)
		}
		if gotArgs[0].(int64) != 4 || gotArgs[1].(int64) != 2 {
			t.Errorf("args = %v, want [4 2]", gotArgs)
		}
	})
	t.Run("absent member", func(t *testing.T) {
		fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
			return fakeRow{err: pgx.ErrNoRows}
		}}
		if _, err := New().GetRole(context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 2)); !errors.Is(err, ErrMembershipNotFound) {
			t.Fatalf("GetRole(absent): got %v, want ErrMembershipNotFound", err)
		}
	})
	t.Run("unknown label is a hard error", func(t *testing.T) {
		fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
			return fakeRow{values: []any{"superuser"}}
		}}
		if _, err := New().GetRole(context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 2)); err == nil {
			t.Fatal("GetRole: want an error for an unknown role label")
		}
	})
}

func TestUpdateRole(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{exec: func(sql string, args ...any) (pgconn.CommandTag, error) {
		gotSQL, gotArgs = sql, args
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}}

	if err := New().UpdateRole(context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 2), domain.MembershipRoleAdmin); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if gotArgs[0].(int64) != 4 || gotArgs[1].(int64) != 2 || gotArgs[2].(string) != "admin" {
		t.Errorf("args = %v, want [4 2 admin]", gotArgs)
	}
	if !strings.Contains(gotSQL, "$3::membership_role") {
		t.Errorf("UpdateRole SQL must cast the role param to the enum: %s", gotSQL)
	}
}

func TestUpdateRole_NotFoundIsStrict(t *testing.T) {
	fq := &fakeQuerier{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}}
	err := New().UpdateRole(context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 2), domain.MembershipRoleMember)
	if !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("UpdateRole(absent): got %v, want ErrMembershipNotFound", err)
	}
}

func TestTransferOwnership(t *testing.T) {
	// One all-or-nothing statement: the target's membership gates the demote,
	// the demote gates the promote, so the writes land together even when q is
	// an autocommitting pool.
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		gotSQL, gotArgs = sql, args
		return fakeRow{values: []any{true, true}}
	}}

	if err := New().TransferOwnership(context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 1), mustUserID(t, 2)); err != nil {
		t.Fatalf("TransferOwnership: %v", err)
	}
	if gotArgs[0].(int64) != 4 || gotArgs[1].(int64) != 1 || gotArgs[2].(int64) != 2 {
		t.Errorf("args = %v, want [4 1 2]", gotArgs)
	}
	// The demote must be gated on the target's membership and the promote on the
	// demote, so a broken precondition changes nothing.
	if !strings.Contains(gotSQL, "EXISTS (SELECT 1 FROM target)") || !strings.Contains(gotSQL, "EXISTS (SELECT 1 FROM demoted)") {
		t.Errorf("TransferOwnership SQL must chain target -> demote -> promote: %s", gotSQL)
	}
}

func TestTransferOwnership_SelfNoop(t *testing.T) {
	if err := New().TransferOwnership(context.Background(), &fakeQuerier{}, mustRoomID(t, 4), mustUserID(t, 1), mustUserID(t, 1)); err != nil {
		t.Fatalf("TransferOwnership(self): %v", err)
	}
}

func TestTransferOwnership_BrokenPreconditionsAreHardErrors(t *testing.T) {
	tests := []struct {
		name         string
		targetExists bool
		promoted     bool
	}{
		{name: "to is not a member", targetExists: false, promoted: false},
		{name: "from does not own the room", targetExists: true, promoted: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
				return fakeRow{values: []any{tc.targetExists, tc.promoted}}
			}}
			err := New().TransferOwnership(context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 1), mustUserID(t, 2))
			if err == nil {
				t.Fatal("TransferOwnership: want a hard error on a broken precondition")
			}
			if errors.Is(err, ErrMembershipNotFound) || errors.Is(err, ErrOwnerCannotLeave) {
				t.Fatalf("TransferOwnership: %v must not be a recoverable sentinel", err)
			}
		})
	}
}

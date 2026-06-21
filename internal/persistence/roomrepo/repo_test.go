package roomrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// fakeQuerier is an in-memory postgres.Querier for exercising the repo's control
// flow without a database. queryRow drives the single-row paths (Create,
// FindByPublicCode); query drives the multi-row paths (ListActive, ListByIDs).
type fakeQuerier struct {
	queryRow func(sql string, args ...any) pgx.Row
	query    func(sql string, args ...any) (pgx.Rows, error)
}

var _ postgres.Querier = (*fakeQuerier)(nil)

// Exec is unused by roomrepo (it only reads via QueryRow/Query); it exists to
// satisfy postgres.Querier.
func (f *fakeQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *fakeQuerier) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	return f.query(sql, args...)
}

func (f *fakeQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	return f.queryRow(sql, args...)
}

type fakeRow struct{ scan func(dest ...any) error }

func (r fakeRow) Scan(dest ...any) error { return r.scan(dest...) }

// fakeRows replays a fixed set of rows (each a slice of column values in the
// scanRoom order) through the pgx.Rows contract collectRooms depends on.
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
// scanRoom passes (*int64, *string, *time.Time).
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

// roomValues builds one row in the scanRoom column order.
func roomValues(rid int64, code, name string, createdBy int64, status string) []any {
	ts := time.Unix(0, 0).UTC()
	return []any{rid, code, name, createdBy, status, ts, ts}
}

type stubIDSource struct {
	id  int64
	err error
}

func (s *stubIDSource) Next() (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.id, nil
}

func mustUserID(t *testing.T, v int64) id.UserID {
	t.Helper()
	uid, err := id.NewUserID(v)
	if err != nil {
		t.Fatalf("NewUserID(%d): %v", v, err)
	}
	return uid
}

func mustRoomID(t *testing.T, v int64) id.RoomID {
	t.Helper()
	rid, err := id.NewRoomID(v)
	if err != nil {
		t.Fatalf("NewRoomID(%d): %v", v, err)
	}
	return rid
}

func TestCreate_Success(t *testing.T) {
	const rid int64 = 9000001
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		gotSQL, gotArgs = sql, args
		return fakeRow{scan: func(dest ...any) error {
			// RETURNING echoes the inserted row back, status defaulted to active.
			return assignAll(dest, roomValues(args[0].(int64), args[1].(string), args[2].(string), args[3].(int64), "active"))
		}}
	}}

	room, err := New(&stubIDSource{id: rid}).Create(context.Background(), fq, CreateParams{
		DisplayName: "General", CreatedBy: mustUserID(t, 1),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if room.ID.Int64() != rid {
		t.Errorf("ID = %d, want %d", room.ID.Int64(), rid)
	}
	if room.DisplayName != "General" {
		t.Errorf("DisplayName = %q, want General", room.DisplayName)
	}
	if room.Status != RoomStatusActive {
		t.Errorf("Status = %q, want active", room.Status)
	}
	if room.CreatedBy.Int64() != 1 {
		t.Errorf("CreatedBy = %d, want 1", room.CreatedBy.Int64())
	}
	if !strings.HasPrefix(room.PublicID(), "R") {
		t.Errorf("PublicID = %q, want an R… code", room.PublicID())
	}
	if gotArgs[0].(int64) != rid || gotArgs[2].(string) != "General" || gotArgs[3].(int64) != 1 {
		t.Errorf("insert args = %v", gotArgs)
	}
	if !strings.Contains(gotSQL, "INSERT INTO room") || !strings.Contains(gotSQL, "'active'") {
		t.Errorf("unexpected insert SQL: %s", gotSQL)
	}
}

func TestCreate_MintErrorSkipsDB(t *testing.T) {
	sentinel := errors.New("lease expired")
	calls := 0
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		calls++
		return fakeRow{scan: func(...any) error { return nil }}
	}}

	_, err := New(&stubIDSource{err: sentinel}).Create(context.Background(), fq, CreateParams{
		DisplayName: "x", CreatedBy: mustUserID(t, 1),
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Create: got %v, want the mint error", err)
	}
	if calls != 0 {
		t.Fatalf("queried %d times, want 0 (mint fails before any DB call)", calls)
	}
}

func TestCreate_ReportsPublicCodeCollision(t *testing.T) {
	// Create does not retry in place (that would break inside a caller's
	// transaction); it reports the collision so the caller re-runs the operation.
	calls := 0
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		calls++
		return fakeRow{scan: func(...any) error {
			return &pgconn.PgError{Code: uniqueViolation, ConstraintName: publicCodeConstraint}
		}}
	}}

	_, err := New(&stubIDSource{id: 7}).Create(context.Background(), fq, CreateParams{
		DisplayName: "x", CreatedBy: mustUserID(t, 1),
	})
	if !errors.Is(err, ErrPublicCodeCollision) {
		t.Fatalf("Create: got %v, want ErrPublicCodeCollision", err)
	}
	if calls != 1 {
		t.Fatalf("queried %d times, want 1 (Create does not retry internally)", calls)
	}
}

func TestCreate_PrimaryKeyCollisionIsHardError(t *testing.T) {
	// A 23505 on a different constraint (a duplicate minted RoomID) is not a
	// public_code clash: it must surface as a hard error, not a recoverable
	// collision the caller would retry with the same id.
	calls := 0
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		calls++
		return fakeRow{scan: func(...any) error {
			return &pgconn.PgError{Code: uniqueViolation, ConstraintName: "room_pkey"}
		}}
	}}

	_, err := New(&stubIDSource{id: 7}).Create(context.Background(), fq, CreateParams{
		DisplayName: "x", CreatedBy: mustUserID(t, 1),
	})
	if err == nil {
		t.Fatal("Create: want an error for a primary-key collision")
	}
	if errors.Is(err, ErrPublicCodeCollision) {
		t.Fatal("a primary-key collision must not be reported as a public_code collision")
	}
	if calls != 1 {
		t.Fatalf("queried %d times, want 1", calls)
	}
}

func TestCreate_PropagatesHardError(t *testing.T) {
	sentinel := errors.New("db down")
	calls := 0
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		calls++
		return fakeRow{scan: func(...any) error { return sentinel }}
	}}

	_, err := New(&stubIDSource{id: 7}).Create(context.Background(), fq, CreateParams{
		DisplayName: "x", CreatedBy: mustUserID(t, 1),
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Create: got %v, want the db error", err)
	}
	if calls != 1 {
		t.Fatalf("queried %d times, want 1 (a non-unique error is not retried)", calls)
	}
}

func TestFindByPublicCode_NotFound(t *testing.T) {
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(...any) error { return pgx.ErrNoRows }}
	}}
	code, _ := id.NewPublicCode()

	_, err := New(nil).FindByPublicCode(context.Background(), fq, code)
	if !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("FindByPublicCode: got %v, want ErrRoomNotFound", err)
	}
}

func TestFindByPublicCode_Success(t *testing.T) {
	code, _ := id.NewPublicCode()
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		if args[0].(string) != code.String() {
			t.Errorf("lookup arg = %q, want %q", args[0], code)
		}
		return fakeRow{scan: func(dest ...any) error {
			return assignAll(dest, roomValues(42, code.String(), "General", 1, "active"))
		}}
	}}

	room, err := New(nil).FindByPublicCode(context.Background(), fq, code)
	if err != nil {
		t.Fatalf("FindByPublicCode: %v", err)
	}
	if room.ID.Int64() != 42 || room.PublicCode.String() != code.String() {
		t.Errorf("room = %+v", room)
	}
}

func TestListActive(t *testing.T) {
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		return &fakeRows{rows: [][]any{
			roomValues(4, "G000000004", "General", 1, "active"),
			roomValues(5, "H000000005", "Random", 2, "active"),
		}}, nil
	}}

	rooms, err := New(nil).ListActive(context.Background(), fq, 0)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(rooms) != 2 || rooms[0].ID.Int64() != 4 || rooms[1].DisplayName != "Random" {
		t.Fatalf("rooms = %+v", rooms)
	}
}

func TestListByIDs_EmptyInputSkipsQuery(t *testing.T) {
	fq := &fakeQuerier{query: func(string, ...any) (pgx.Rows, error) {
		t.Fatal("ListByIDs queried the DB for an empty id slice")
		return nil, nil
	}}
	rooms, err := New(nil).ListByIDs(context.Background(), fq, nil)
	if err != nil || rooms != nil {
		t.Fatalf("ListByIDs(nil) = %v, %v; want nil, nil", rooms, err)
	}
}

func TestListByIDs(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		gotSQL, gotArgs = sql, args
		return &fakeRows{rows: [][]any{roomValues(4, "G000000004", "General", 1, "active")}}, nil
	}}

	rooms, err := New(nil).ListByIDs(context.Background(), fq, []id.RoomID{mustRoomID(t, 4), mustRoomID(t, 5)})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if len(rooms) != 1 || rooms[0].ID.Int64() != 4 {
		t.Fatalf("rooms = %+v", rooms)
	}
	ids, ok := gotArgs[0].([]int64)
	if !ok || len(ids) != 2 || ids[0] != 4 || ids[1] != 5 {
		t.Fatalf("args[0] = %v, want []int64{4, 5} for = ANY($1)", gotArgs[0])
	}
	// Archived rooms must never reach the client through the joined-rooms mapping.
	if !strings.Contains(gotSQL, "status = 'active'") {
		t.Errorf("ListByIDs SQL is missing the active filter: %s", gotSQL)
	}
}

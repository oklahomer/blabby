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

	"github.com/oklahomer/blabby/internal/domain"
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

func mustRoomName(t *testing.T, raw string) domain.RoomName {
	t.Helper()
	name, err := domain.NewRoomName(raw)
	if err != nil {
		t.Fatalf("NewRoomName(%q): %v", raw, err)
	}
	return name
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
		Name: mustRoomName(t, "General"), CreatedBy: mustUserID(t, 1),
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
	if room.Status != domain.RoomStatusActive {
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
		Name: mustRoomName(t, "x"), CreatedBy: mustUserID(t, 1),
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
		Name: mustRoomName(t, "x"), CreatedBy: mustUserID(t, 1),
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
		Name: mustRoomName(t, "x"), CreatedBy: mustUserID(t, 1),
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
		Name: mustRoomName(t, "x"), CreatedBy: mustUserID(t, 1),
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

func TestFindByID_Success(t *testing.T) {
	var gotSQL string
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		gotSQL = sql
		if args[0].(int64) != 42 {
			t.Errorf("lookup arg = %v, want 42", args[0])
		}
		return fakeRow{scan: func(dest ...any) error {
			return assignAll(dest, roomValues(42, "G000000042", "General", 1, "active"))
		}}
	}}

	room, err := New(nil).FindByID(context.Background(), fq, mustRoomID(t, 42))
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if room.ID.Int64() != 42 || room.Status != domain.RoomStatusActive {
		t.Errorf("room = %+v", room)
	}
	// FindByID loads regardless of status, so unlike the active-only lookups it
	// must not carry the active filter.
	if strings.Contains(gotSQL, "status = 'active'") {
		t.Errorf("FindByID SQL must not filter on active status: %s", gotSQL)
	}
}

func TestFindByID_ReturnsArchivedRoom(t *testing.T) {
	// The differentiator from FindByPublicCode/ListByIDs: an archived room is
	// surfaced (not hidden) so the Room grain can see it and reject commands with
	// ROOM_NOT_FOUND rather than treating it as never having existed.
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		return fakeRow{scan: func(dest ...any) error {
			return assignAll(dest, roomValues(7, "G000000007", "Archived", 1, "archived"))
		}}
	}}

	room, err := New(nil).FindByID(context.Background(), fq, mustRoomID(t, 7))
	if err != nil {
		t.Fatalf("FindByID(archived): got err %v, want the archived room", err)
	}
	if room.Status != domain.RoomStatusArchived {
		t.Errorf("Status = %q, want archived", room.Status)
	}
}

func TestFindByID_NotFound(t *testing.T) {
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(...any) error { return pgx.ErrNoRows }}
	}}

	_, err := New(nil).FindByID(context.Background(), fq, mustRoomID(t, 99))
	if !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("FindByID(missing): got %v, want ErrRoomNotFound", err)
	}
}

func mustRoomNameQuery(t *testing.T, raw string) domain.RoomNameQuery {
	t.Helper()
	q, err := domain.NewRoomNameQuery(raw)
	if err != nil {
		t.Fatalf("NewRoomNameQuery(%q): %v", raw, err)
	}
	return q
}

func TestListActive_FirstPageWithoutFilter(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		gotSQL, gotArgs = sql, args
		return &fakeRows{rows: [][]any{
			roomValues(4, "G000000004", "General", 1, "active"),
			roomValues(5, "H000000005", "Random", 2, "active"),
		}}, nil
	}}

	rooms, hasMore, err := New(nil).ListActive(context.Background(), fq, ListActiveParams{Limit: 2})
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(rooms) != 2 || rooms[0].ID.Int64() != 4 || rooms[1].DisplayName != "Random" {
		t.Fatalf("rooms = %+v", rooms)
	}
	// Exactly Limit rows came back, so the Limit+1 look-ahead found no next page.
	if hasMore {
		t.Error("hasMore = true, want false when the look-ahead row is absent")
	}
	if !strings.Contains(gotSQL, "status = 'active'") {
		t.Errorf("SQL is missing the active filter: %s", gotSQL)
	}
	if !strings.Contains(gotSQL, "ORDER BY id") {
		t.Errorf("SQL is missing the keyset ordering: %s", gotSQL)
	}
	if strings.Contains(gotSQL, "ILIKE") || strings.Contains(gotSQL, "id >") {
		t.Errorf("SQL carries filter/cursor clauses for zero params: %s", gotSQL)
	}
	// The look-ahead row is the only argument: LIMIT Limit+1.
	if len(gotArgs) != 1 || gotArgs[0].(int) != 3 {
		t.Errorf("args = %v, want [3] (Limit+1)", gotArgs)
	}
}

func TestListActive_LookAheadRowSetsHasMore(t *testing.T) {
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		return &fakeRows{rows: [][]any{
			roomValues(4, "G000000004", "General", 1, "active"),
			roomValues(5, "H000000005", "Random", 2, "active"),
			roomValues(6, "J000000006", "Lounge", 1, "active"),
		}}, nil
	}}

	rooms, hasMore, err := New(nil).ListActive(context.Background(), fq, ListActiveParams{Limit: 2})
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	// The look-ahead row proves a next page exists but must not be returned.
	if len(rooms) != 2 || rooms[1].ID.Int64() != 5 {
		t.Fatalf("rooms = %+v, want exactly the first 2", rooms)
	}
	if !hasMore {
		t.Error("hasMore = false, want true when the look-ahead row is present")
	}
}

func TestListActive_FilterAndCursorClauses(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		gotSQL, gotArgs = sql, args
		return &fakeRows{}, nil
	}}

	_, _, err := New(nil).ListActive(context.Background(), fq, ListActiveParams{
		Query:   mustRoomNameQuery(t, "Gen"),
		AfterID: mustRoomID(t, 4),
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if !strings.Contains(gotSQL, "display_name ILIKE $1") {
		t.Errorf("SQL is missing the name filter: %s", gotSQL)
	}
	if !strings.Contains(gotSQL, "id > $2") {
		t.Errorf("SQL is missing the keyset cursor: %s", gotSQL)
	}
	if !strings.Contains(gotSQL, "LIMIT $3") {
		t.Errorf("SQL is missing the limit: %s", gotSQL)
	}
	if len(gotArgs) != 3 || gotArgs[0].(string) != "%Gen%" || gotArgs[1].(int64) != 4 || gotArgs[2].(int) != 6 {
		t.Errorf("args = %v, want [%%Gen%% 4 6]", gotArgs)
	}
}

func TestListActive_EscapesLikeWildcards(t *testing.T) {
	// LIKE's wildcards and its escape character in the fragment must match
	// literally, or a q of "%" would match every room.
	var gotArgs []any
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		gotArgs = args
		return &fakeRows{}, nil
	}}

	_, _, err := New(nil).ListActive(context.Background(), fq, ListActiveParams{
		Query: mustRoomNameQuery(t, `100%_a\b`),
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	want := `%100\%\_a\\b%`
	if gotArgs[0].(string) != want {
		t.Errorf("pattern = %q, want %q", gotArgs[0], want)
	}
}

func TestListActive_PropagatesQueryError(t *testing.T) {
	boom := errors.New("connection reset")
	fq := &fakeQuerier{query: func(string, ...any) (pgx.Rows, error) { return nil, boom }}

	_, _, err := New(nil).ListActive(context.Background(), fq, ListActiveParams{Limit: 5})
	if !errors.Is(err, boom) {
		t.Fatalf("ListActive err = %v, want wrapped %v", err, boom)
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

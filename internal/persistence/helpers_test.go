package persistence

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// fakeQuerier is an in-memory postgres.Querier for exercising a repo's control flow
// without a database. queryRow drives single-row reads, query drives multi-row
// reads, and exec drives writes; each repo's tests set only the fields it uses.
type fakeQuerier struct {
	queryRow func(sql string, args ...any) pgx.Row
	query    func(sql string, args ...any) (pgx.Rows, error)
	exec     func(sql string, args ...any) (pgconn.CommandTag, error)
}

var _ postgres.Querier = (*fakeQuerier)(nil)

func (f *fakeQuerier) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return f.exec(sql, args...)
}

func (f *fakeQuerier) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	return f.query(sql, args...)
}

func (f *fakeQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	return f.queryRow(sql, args...)
}

// fakeRow is a one-row pgx.Row stub. Set scan to compute the result (e.g. to
// capture query args or return a custom error); or set values for a fixed row, or
// err for a fixed failure.
type fakeRow struct {
	scan   func(dest ...any) error
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	switch {
	case r.scan != nil:
		return r.scan(dest...)
	case r.err != nil:
		return r.err
	default:
		return assignAll(dest, r.values)
	}
}

// fakeRows replays a fixed set of rows (each a slice of column values in scan
// order) through the pgx.Rows contract the multi-row collect helpers depend on.
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

// assignAll copies column values into the Scan destinations, matching the pointer
// types the scan helpers across the repos pass.
func assignAll(dest []any, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("fake scan: %d destinations, %d values", len(dest), len(values))
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *int64:
			*d = values[i].(int64)
		case *int:
			*d = values[i].(int)
		case *string:
			*d = values[i].(string)
		case *bool:
			*d = values[i].(bool)
		case *[]byte:
			*d = values[i].([]byte)
		case *time.Time:
			*d = values[i].(time.Time)
		case **time.Time:
			*d = values[i].(*time.Time)
		default:
			return fmt.Errorf("fake scan: unsupported destination %T", dest[i])
		}
	}
	return nil
}

// stubIDSource mints a fixed id, or an error to drive the mint-failure path. Its
// value receiver satisfies the id-source interfaces by value or by pointer.
type stubIDSource struct {
	id  int64
	err error
}

func (s stubIDSource) Next() (int64, error) {
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

func mustUserRef(t *testing.T, rawID int64, name string) id.UserRef {
	t.Helper()
	uid, err := id.NewUserID(rawID)
	if err != nil {
		t.Fatalf("NewUserID(%d): %v", rawID, err)
	}
	code, err := id.NewPublicCode()
	if err != nil {
		t.Fatalf("NewPublicCode: %v", err)
	}
	ref, err := id.NewUserRef(uid, code, name)
	if err != nil {
		t.Fatalf("NewUserRef(%d,%q): %v", rawID, name, err)
	}
	return ref
}

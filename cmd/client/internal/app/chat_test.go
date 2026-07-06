package app

import (
	"testing"

	"github.com/oklahomer/blabby/cmd/client/internal/panes/mainview"
	"github.com/oklahomer/blabby/cmd/client/internal/timeline"
)

func msgID(id int64) mainview.Message {
	return mainview.Message{ID: timeline.EventID(id), Kind: mainview.KindChat}
}

func ids(bucket []mainview.Message) []int64 {
	out := make([]int64, len(bucket))
	for i, m := range bucket {
		out[i] = m.ID.Int64()
	}
	return out
}

func TestInsertOrderedByEventID(t *testing.T) {
	var bucket []mainview.Message
	var inserted bool
	for _, id := range []int64{30, 10, 20} {
		bucket, inserted, _ = insertOrdered(bucket, msgID(id), 100)
		if !inserted {
			t.Fatalf("id %d reported not inserted", id)
		}
	}
	got := ids(bucket)
	want := []int64{10, 20, 30}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestInsertOrderedDedups(t *testing.T) {
	bucket, _, _ := insertOrdered(nil, msgID(5), 100)
	bucket, inserted, trimmed := insertOrdered(bucket, msgID(5), 100)
	if inserted {
		t.Fatal("re-inserting the same id must report inserted=false")
	}
	if trimmed != 0 {
		t.Fatalf("dedup must not trim, got trimmed=%d", trimmed)
	}
	if len(bucket) != 1 {
		t.Fatalf("expected 1 entry after dedup, got %d", len(bucket))
	}
}

func TestInsertOrderedTrimsOldestOverCap(t *testing.T) {
	var bucket []mainview.Message
	for _, id := range []int64{1, 2, 3} {
		bucket, _, _ = insertOrdered(bucket, msgID(id), 3)
	}
	// Cap is 3; inserting a 4th trims exactly one (the oldest, id 1).
	bucket, inserted, trimmed := insertOrdered(bucket, msgID(4), 3)
	if !inserted || trimmed != 1 {
		t.Fatalf("inserted=%v trimmed=%d, want true/1", inserted, trimmed)
	}
	got := ids(bucket)
	want := []int64{2, 3, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestInsertOrderedDoesNotAliasInput(t *testing.T) {
	// A fresh backing array must be allocated so a value-copied Model cannot
	// mutate another copy's scrollback through a shared slice.
	original, _, _ := insertOrdered(nil, msgID(10), 100)
	original, _, _ = insertOrdered(original, msgID(20), 100)
	next, _, _ := insertOrdered(original, msgID(15), 100)
	if len(original) != 2 {
		t.Fatalf("insert mutated the input slice length: %v", ids(original))
	}
	if ids(original)[0] != 10 || ids(original)[1] != 20 {
		t.Fatalf("insert mutated the input slice contents: %v", ids(original))
	}
	if len(next) != 3 {
		t.Fatalf("expected 3 in the new slice, got %v", ids(next))
	}
}

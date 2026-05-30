package roomsearch

import (
	"reflect"
	"testing"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

func TestVisibleEmptyQueryReturnsAll(t *testing.T) {
	rooms := []api.Room{
		{ID: "general", Name: "General"},
		{ID: "random", Name: "Random"},
	}
	got := Visible(rooms, "")
	if !reflect.DeepEqual(got, rooms) {
		t.Fatalf("got %#v, want %#v", got, rooms)
	}
}

func TestVisibleCaseInsensitiveSubstringName(t *testing.T) {
	rooms := []api.Room{
		{ID: "general", Name: "General"},
		{ID: "random", Name: "Random"},
	}
	got := Visible(rooms, "GEN")
	if len(got) != 1 || got[0].ID != "general" {
		t.Fatalf("expected single general row, got %#v", got)
	}
}

func TestVisibleCaseInsensitiveSubstringID(t *testing.T) {
	rooms := []api.Room{
		{ID: "general", Name: "General"},
		{ID: "random", Name: "Random"},
	}
	got := Visible(rooms, "ANDOM")
	if len(got) != 1 || got[0].ID != "random" {
		t.Fatalf("expected single random row, got %#v", got)
	}
}

func TestVisibleNoMatchReturnsEmpty(t *testing.T) {
	rooms := []api.Room{
		{ID: "general", Name: "General"},
	}
	got := Visible(rooms, "zzz")
	if len(got) != 0 {
		t.Fatalf("expected no matches, got %#v", got)
	}
}

func TestVisiblePreservesServerOrder(t *testing.T) {
	rooms := []api.Room{
		{ID: "zulu", Name: "Zulu"},
		{ID: "alpha", Name: "Alpha"},
		{ID: "bravo", Name: "Bravo"},
	}
	got := Visible(rooms, "a")
	// "a" appears in alpha, bravo, and Zulu has none — alpha then bravo.
	if len(got) != 2 || got[0].ID != "alpha" || got[1].ID != "bravo" {
		t.Fatalf("server order broken: %#v", got)
	}
}

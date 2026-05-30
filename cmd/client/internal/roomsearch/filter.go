package roomsearch

import (
	"strings"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// Visible returns the subset of rooms whose name or id contains query
// (case-insensitive, substring match). The server's return order is
// preserved across the filtered subset — never re-sorted.
//
// An empty query is treated as "match all" and returns the slice
// verbatim so the caller can share the backing array. A non-empty
// query that matches nothing returns nil (zero-length, nil backing
// array) so the caller can branch on len(...) == 0 without separately
// inspecting capacity.
func Visible(rooms []api.Room, query string) []api.Room {
	if query == "" {
		return rooms
	}
	needle := strings.ToLower(query)
	var matches []api.Room
	for _, r := range rooms {
		if strings.Contains(strings.ToLower(r.Name), needle) ||
			strings.Contains(strings.ToLower(r.ID), needle) {
			matches = append(matches, r)
		}
	}
	return matches
}

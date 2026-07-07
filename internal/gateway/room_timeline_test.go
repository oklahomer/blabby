package gateway

import (
	"context"
	"strings"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
)

// stubRoomTimeline is an in-memory RoomTimeline seeded with the dev membership
// (user 1 is a member of room 4), mirroring stubRoomDirectory's seed. Entries
// are per-room, held newest-first as Events returns them. Setting an error
// field makes the corresponding method fail, to exercise the 503 paths.
type stubRoomTimeline struct {
	members   map[int64][]int64
	entries   map[int64][]persistence.TimelineEntry
	memberErr error
	eventsErr error
}

func newStubRoomTimeline() *stubRoomTimeline {
	return &stubRoomTimeline{
		members: map[int64][]int64{4: {1}},
		entries: map[int64][]persistence.TimelineEntry{},
	}
}

func (s *stubRoomTimeline) IsMember(_ context.Context, roomID id.RoomID, userID id.UserID) (bool, error) {
	if s.memberErr != nil {
		return false, s.memberErr
	}
	for _, member := range s.members[roomID.Int64()] {
		if member == userID.Int64() {
			return true, nil
		}
	}
	return false, nil
}

// Events mirrors the production contract in memory: newest-first, before-id
// keyset cursor, message-only substring filter, and a look-ahead HasMore.
func (s *stubRoomTimeline) Events(_ context.Context, query TimelineQuery) (TimelinePage, error) {
	if s.eventsErr != nil {
		return TimelinePage{}, s.eventsErr
	}
	needle := ""
	if !query.Query.IsZero() {
		needle = strings.ToLower(query.Query.String())
	}
	var matched []persistence.TimelineEntry
	for _, entry := range s.entries[query.RoomID.Int64()] {
		if query.Before != (id.EventID{}) && entry.ID.Int64() >= query.Before.Int64() {
			continue
		}
		if needle != "" &&
			(entry.Kind != persistence.EntryMessage || !strings.Contains(strings.ToLower(entry.Text), needle)) {
			continue
		}
		matched = append(matched, entry)
	}
	if len(matched) > query.Limit {
		return TimelinePage{Events: matched[:query.Limit], HasMore: true}, nil
	}
	return TimelinePage{Events: matched}, nil
}

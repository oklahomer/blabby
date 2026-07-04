package gateway

import (
	"context"
	"strings"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/roomrepo"
)

// stubRoomDirectory is an in-memory RoomDirectory seeded with the dev rooms
// (room 4 → RG000000004 "General", room 5 → RH000000005 "Random"), matching the
// persistence seed. Setting err makes every method fail, to exercise the 503 path.
type stubRoomDirectory struct {
	byCode  map[string]RoomInfo
	ordered []RoomInfo
	err     error
}

func newStubRoomDirectory() *stubRoomDirectory {
	d := &stubRoomDirectory{byCode: map[string]RoomInfo{}}
	d.add(4, "G000000004", "General")
	d.add(5, "H000000005", "Random")
	return d
}

func (d *stubRoomDirectory) add(rawID int64, code, name string) {
	rid, err := id.NewRoomID(rawID)
	if err != nil {
		panic(err)
	}
	c, err := id.ParsePublicCode(code)
	if err != nil {
		panic(err)
	}
	info := RoomInfo{ID: rid, Code: c, Name: name}
	d.byCode[c.String()] = info
	d.ordered = append(d.ordered, info)
}

func (d *stubRoomDirectory) Resolve(_ context.Context, code id.PublicCode) (id.RoomID, error) {
	if d.err != nil {
		return id.RoomID{}, d.err
	}
	info, ok := d.byCode[code.String()]
	if !ok {
		return id.RoomID{}, roomrepo.ErrRoomNotFound
	}
	return info.ID, nil
}

// ListActive mirrors the production contract in memory: case-insensitive
// substring filter, id-keyset cursor, and a look-ahead HasMore.
func (d *stubRoomDirectory) ListActive(_ context.Context, query ListActiveQuery) (RoomPage, error) {
	if d.err != nil {
		return RoomPage{}, d.err
	}
	var matched []RoomInfo
	for _, info := range d.ordered {
		if !query.Query.IsZero() &&
			!strings.Contains(strings.ToLower(info.Name), strings.ToLower(query.Query.String())) {
			continue
		}
		if info.ID.Int64() <= query.After.Int64() {
			continue
		}
		matched = append(matched, info)
	}
	if len(matched) > query.Limit {
		return RoomPage{Rooms: matched[:query.Limit], HasMore: true}, nil
	}
	return RoomPage{Rooms: matched}, nil
}

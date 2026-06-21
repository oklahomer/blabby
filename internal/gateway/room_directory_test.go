package gateway

import (
	"context"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/roomrepo"
)

// stubRoomDirectory is an in-memory RoomDirectory seeded with the dev rooms
// (room 4 → RG000000004 "General", room 5 → RH000000005 "Random"), matching the
// persistence seed. Setting err makes every method fail, to exercise the 503 path.
type stubRoomDirectory struct {
	byCode  map[string]RoomInfo
	byID    map[id.RoomID]RoomInfo
	ordered []RoomInfo
	err     error
}

func newStubRoomDirectory() *stubRoomDirectory {
	d := &stubRoomDirectory{byCode: map[string]RoomInfo{}, byID: map[id.RoomID]RoomInfo{}}
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
	d.byID[rid] = info
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

func (d *stubRoomDirectory) ListActive(_ context.Context) ([]RoomInfo, error) {
	if d.err != nil {
		return nil, d.err
	}
	out := make([]RoomInfo, len(d.ordered))
	copy(out, d.ordered)
	return out, nil
}

func (d *stubRoomDirectory) Describe(_ context.Context, ids []id.RoomID) ([]RoomInfo, error) {
	if d.err != nil {
		return nil, d.err
	}
	var out []RoomInfo
	for _, rid := range ids {
		if info, ok := d.byID[rid]; ok {
			out = append(out, info)
		}
	}
	return out, nil
}

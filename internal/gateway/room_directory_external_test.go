package gateway_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/gateway"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
)

// stubUserDirectory is a user.Directory for the cluster-backed integration
// tests: it resolves every id to a UserRef with a valid public code, so the
// User grain's self carries the public identity the Room grain and the
// connection now require (a code-less self fails closed and drops frames).
type stubUserDirectory struct{}

func (stubUserDirectory) Resolve(_ context.Context, uid id.UserID) (domain.UserRef, error) {
	code, err := id.ParsePublicCode("A000000001")
	if err != nil {
		return domain.UserRef{}, err
	}
	return domain.NewUserRef(uid, code, fmt.Sprintf("user-%s", uid))
}

// stubRoomDirectory is an in-memory gateway.RoomDirectory for the external
// integration tests, seeded with the dev rooms (room 4 → RG000000004 "General",
// room 5 → RH000000005 "Random") so /rooms/{id} resolution and the catalogue work
// without a database.
type stubRoomDirectory struct {
	byCode  map[string]gateway.RoomInfo
	ordered []gateway.RoomInfo
}

func newStubRoomDirectory() *stubRoomDirectory {
	d := &stubRoomDirectory{byCode: map[string]gateway.RoomInfo{}}
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
	info := gateway.RoomInfo{ID: rid, Code: c, Name: name}
	d.byCode[c.String()] = info
	d.ordered = append(d.ordered, info)
}

func (d *stubRoomDirectory) Resolve(_ context.Context, code id.PublicCode) (id.RoomID, error) {
	info, ok := d.byCode[code.String()]
	if !ok {
		return id.RoomID{}, persistence.ErrRoomNotFound
	}
	return info.ID, nil
}

// ListActive mirrors the production contract in memory: case-insensitive
// substring filter, id-keyset cursor, and a look-ahead HasMore.
func (d *stubRoomDirectory) ListActive(_ context.Context, query gateway.ListActiveQuery) (gateway.RoomPage, error) {
	needle := ""
	if !query.Query.IsZero() {
		needle = strings.ToLower(query.Query.String())
	}
	var matched []gateway.RoomInfo
	for _, info := range d.ordered {
		if needle != "" && !strings.Contains(strings.ToLower(info.Name), needle) {
			continue
		}
		if info.ID.Int64() <= query.After.Int64() {
			continue
		}
		matched = append(matched, info)
	}
	if len(matched) > query.Limit {
		return gateway.RoomPage{Rooms: matched[:query.Limit], HasMore: true}, nil
	}
	return gateway.RoomPage{Rooms: matched}, nil
}

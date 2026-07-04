package gateway

import (
	"context"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/persistence/roomrepo"
)

// RoomInfo is the gateway's view of a room for the catalogue: the internal id,
// the opaque public_code (the R… the client sees), and the display name. No
// internal numeric id is rendered to the client — only Code crosses, as R<code>.
type RoomInfo struct {
	ID   id.RoomID
	Code id.PublicCode
	Name string
}

// PublicID renders the room's client-facing R… code.
func (ri RoomInfo) PublicID() string { return ri.Code.FormatRoom() }

// ListActiveQuery filters and paginates the catalogue listing. The zero values
// of Query and After mean "no name filter" and "first page"; Limit is the page
// size and must be positive (the handler parses and caps it at the boundary).
type ListActiveQuery struct {
	Query domain.RoomNameQuery
	After id.RoomID
	Limit int
}

// RoomPage is one page of the catalogue. HasMore reports whether at least one
// more room follows the last entry, so the handler can emit a continuation
// cursor.
type RoomPage struct {
	Rooms   []RoomInfo
	HasMore bool
}

// RoomDirectory translates the opaque, client-facing room codes (R…) to internal
// RoomIDs and lists rooms for the catalogue. It is the gateway's seam over
// roomrepo, so handlers never touch the database and no internal numeric id
// reaches a client. Resolve reports roomrepo.ErrRoomNotFound for an unknown or
// inactive code.
type RoomDirectory interface {
	Resolve(ctx context.Context, code id.PublicCode) (id.RoomID, error)
	ListActive(ctx context.Context, query ListActiveQuery) (RoomPage, error)
}

// roomRepoDirectory is the production RoomDirectory: a read-only view of the room
// table via roomrepo over the gateway's read pool. The gateway never creates
// rooms, so roomrepo's id source is unused here.
type roomRepoDirectory struct {
	repo *roomrepo.Repo
	pool postgres.Querier
}

// NewRoomRepoDirectory builds a read-only RoomDirectory over pool. It owns the
// roomrepo.Repo internally with a nil id source, because the gateway reads rooms
// but never mints them — so callers never see the unused id source.
func NewRoomRepoDirectory(pool postgres.Querier) RoomDirectory {
	return roomRepoDirectory{repo: roomrepo.New(nil), pool: pool}
}

func (d roomRepoDirectory) Resolve(ctx context.Context, code id.PublicCode) (id.RoomID, error) {
	room, err := d.repo.FindByPublicCode(ctx, d.pool, code)
	if err != nil {
		return id.RoomID{}, err
	}
	return room.ID, nil
}

func (d roomRepoDirectory) ListActive(ctx context.Context, query ListActiveQuery) (RoomPage, error) {
	rooms, hasMore, err := d.repo.ListActive(ctx, d.pool, roomrepo.ListActiveParams{
		Query:   query.Query,
		AfterID: query.After,
		Limit:   query.Limit,
	})
	if err != nil {
		return RoomPage{}, err
	}
	return RoomPage{Rooms: toRoomInfos(rooms), HasMore: hasMore}, nil
}

func toRoomInfos(rooms []roomrepo.Room) []RoomInfo {
	out := make([]RoomInfo, len(rooms))
	for i, r := range rooms {
		out[i] = RoomInfo{ID: r.ID, Code: r.PublicCode, Name: r.DisplayName}
	}
	return out
}

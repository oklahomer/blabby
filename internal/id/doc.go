// Package id defines value-object types for the domain's Snowflake identifiers —
// UserID, RoomID, and EventID — plus the opaque, user-facing public_code.
//
// Each id wraps a positive 63-bit int64 minted by internal/snowflake, distinct at
// the Go type level so a function expecting one rejects another at compile time.
// They are internal values: [ParseUserID] and [ParseRoomID] decode the
// decimal-string form used in proto fields and JSON, while [NewUserID],
// [NewRoomID], and [NewEventID] wrap a value read from a BIGINT column. Where an
// id serializes into JSON it renders as a decimal string, because JavaScript
// cannot hold a 63-bit integer exactly.
//
// User and room entities additionally have a separate, opaque public_code (see
// [PublicCode]) — the only user/room identifier shown to clients, rendered
// U<code>/R<code>. A PublicCode is not part of a UserID/RoomID value; it is stored
// alongside the entity and resolved to the internal id at the gateway. EventID is
// the one id that itself crosses to clients (as a decimal string); it has no
// public_code. Storage-backed entities (Room, User) belong to their own packages.
package id

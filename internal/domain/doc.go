// Package domain holds reference-metadata value types: small, denormalized
// snapshots of entities and relationships that travel across grain messages, read
// models, and gateway rendering.
//
// These are reference metadata, not authoritative identity. Internal routing, map
// keys, foreign keys, grain identity, and authorization use the numeric Snowflake
// ids in internal/id; the refs here add the client-facing metadata (public code,
// display name, lifecycle status) plus a MetadataVersion so a receiver can ignore
// a stale snapshot. The owning entity (its table and grain) is the source of
// truth; a cached ref may be briefly stale, which is harmless because operations
// never route or authorize off ref metadata.
//
// Base refs (RoomRef, UserRef) stay small and compose into relationship refs
// (MembershipRef, JoinedRoomRef) and use-case views. A nested ref never repeats an
// id already carried by its owner or map key.
//
// MetadataVersion is an opaque monotonic value (typically derived from the
// owner's updated_at); receivers compare it and apply only newer snapshots.
package domain

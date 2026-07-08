// Package domain holds reference-metadata value types: small, denormalized
// snapshots of entities and relationships that travel across grain messages, read
// models, and gateway rendering.
//
// These are reference metadata, not authoritative identity. Internal routing, map
// keys, foreign keys, grain identity, and authorization use the numeric Snowflake
// ids in internal/id; the refs here add the client-facing metadata: the public
// code and display name, and (on RoomRef) the lifecycle status plus a
// MetadataVersion so a receiver can ignore a stale snapshot. The owning entity
// (its table and grain) is the source of truth; a cached ref may be briefly
// stale, which is harmless because operations never route or authorize off ref
// metadata.
//
// Refs are encapsulated value objects: constructed through their New… functions,
// which enforce that the internal id and the public code travel together (so the
// internal id never has to stand in for the public one on the client wire) and
// that the display label is present. Base refs stay small so relationship views
// can compose them; a ref nested in a larger value never repeats an id already
// carried by its owner or map key.
//
// MetadataVersion is an opaque monotonic value (typically derived from the
// owner's updated_at); receivers compare it and apply only newer snapshots.
package domain

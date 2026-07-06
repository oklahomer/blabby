# ADR-020: PGroonga search stack — CJK-capable full-text in PostgreSQL, hand-written SQL over pgx

- **Status:** Accepted
- **Date:** 2026-07-05
- **Related:** [ADR-007](adr-007-database-authoritative-persistence.md), [ADR-008](adr-008-no-redis.md)

## Context

Two search surfaces need an index a plain b-tree cannot serve. The room catalogue
(`GET /rooms?q=…`) matches a fragment anywhere inside a room's display name — a
leading-wildcard `ILIKE '%fragment%'` a b-tree cannot use. The message timeline
(`GET /rooms/{id}/events?q=…`) matches messages whose text contains a set of terms —
full-text search over the message body. Both must work for the short CJK fragments a
chat product invites (a two-character Japanese or Chinese query is normal), where
whitespace tokenization and English stemming do not apply.

PostgreSQL's built-in text search (`tsvector`/`tsquery`) is tuned for
whitespace-and-stemming languages and segments CJK poorly without extra
configuration; it also does not serve the substring/leading-wildcard case the room
catalogue needs. The alternative most teams reach for — a separate search engine — is
a whole new tier to run, synchronize, and secure, which cuts against a system that
deliberately avoids extra infrastructure ([ADR-008](adr-008-no-redis.md)).

## Decision

**Search runs inside PostgreSQL via the PGroonga extension, over hand-written
parameterized SQL executed with pgx. Two partial PGroonga indexes cover the two
surfaces, and user input is always treated as literal terms, never a query language.**

- **The extension.** `CREATE EXTENSION pgroonga` ships in the schema
  ([ADR-007](adr-007-database-authoritative-persistence.md)). Groonga's tokenizer
  segments CJK text, so short non-whitespace-delimited fragments match; the same
  operator class also serves `ILIKE` with a leading wildcard, which a b-tree cannot.
- **Room-name search.** A PGroonga index on `room.display_name`, partial on
  `status = 'active'` (archived rooms are never searched), backs a case-insensitive
  substring match: `display_name ILIKE '%fragment%'` with the fragment's `LIKE`
  wildcards escaped so it matches literally.
- **Message-text search.** A PGroonga index on `(payload->>'text')`, partial on
  `type = 'message_posted'` (only messages carry text), backs the full-text operator:
  `(payload->>'text') &@~ $1`. Each whitespace-separated term of the fragment is
  quoted into a literal phrase (backslashes and quotes escaped) and the phrases are
  joined by Groonga's implicit AND, so a query means exactly "every one of these
  terms appears" — an operator-looking word (`OR`, a leading `-`) or stray query
  syntax in user input cannot change the semantics.
- **Access via pgx, no query builder.** The repositories build these statements as
  hand-written SQL with positional `$N` parameters and run them on pgx v5
  (`internal/persistence/journal/timeline.go`, `internal/persistence/roomrepo`). The
  query set is small and the load-bearing parts are the non-standard PGroonga
  operators, which stay explicit and reviewable in raw SQL.

## Consequences

### Positive

- **CJK search works without a language pipeline.** Groonga's tokenizer handles the
  segmentation `tsvector` would need per-language configuration for, so short CJK
  fragments match.
- **One store, one transaction, one dependency to run.** Search indexes live beside
  the data; there is no external engine to deploy, synchronize on every write, or
  secure. A search reads the same PostgreSQL a membership check does.
- **Partial indexes stay small and on-point.** The room index covers only active
  rooms; the message index only message events — the rows each surface actually
  searches.
- **User input cannot inject query syntax.** Literal-term quoting (message) and
  wildcard escaping (room) mean search is a contains-these-terms operation, not a
  query language handed to clients.
- **The SQL reads for itself.** The PGroonga operators (`&@~`, PGroonga-backed
  `ILIKE`) are visible in the statement rather than hidden behind a builder's
  abstraction.

### Negative

- **PGroonga is a non-core extension.** It must be present in the database image
  (the project's PostgreSQL image bundles it); a vanilla PostgreSQL does not have it,
  so the deployment is pinned to an image that does.
- **Search shares the database's resources.** Heavy search load competes with the
  transactional workload on the same instance; there is no separate search tier to
  absorb it. Acceptable at this system's scale, and consistent with the no-extra-tier
  stance ([ADR-008](adr-008-no-redis.md)).
- **Hand-written SQL is not compiler-checked.** A column rename is caught by the
  integration tests, not the type system. Mitigated by the small, centralized query
  set.

### Neutral

- **Ranking is not used.** Both surfaces return matches ordered by id (newest-first
  timeline, id-ordered catalogue), not by a relevance score; the queries ask "does it
  match," not "how well."

## Alternatives considered

### PostgreSQL native full-text (`tsvector` / `tsquery`)

Built into PostgreSQL, no extension. Rejected: its tokenizer targets
whitespace-delimited, stemmed languages and segments CJK poorly without additional
configuration, and it does not serve the leading-wildcard substring match the room
catalogue needs. PGroonga covers both cases with one operator class.

### Trigram index (`pg_trgm`) for `ILIKE`

Serves substring `ILIKE` on Latin text. Rejected as the general answer: trigram
matching degrades for the short CJK fragments chat invites (a one- or two-character
query has too few trigrams to discriminate), and it does not provide the tokenized
full-text matching the message search wants. PGroonga handles both surfaces uniformly.

### A dedicated search engine (Elasticsearch, Meilisearch, …)

Index documents in an external engine. Rejected: a new service to deploy, secure, and
keep in sync with the database on every write — the exact extra tier the architecture
avoids ([ADR-008](adr-008-no-redis.md)). Keeping search in PostgreSQL means a match
reads consistent, committed data with no synchronization lag.

### An ORM or SQL query builder (e.g. squirrel, GORM)

Build the statements programmatically. Rejected: the query set is small and its most
important elements are the non-standard PGroonga operators, which a builder would
either obscure behind raw-expression escape hatches or fail to model at all.
Hand-written parameterized SQL keeps the operators explicit and the statements
reviewable; parameterization (not string interpolation of values) still guards
against injection.

## References

- [ADR-007](adr-007-database-authoritative-persistence.md) — the schema, the JSONB
  message payload, and the PGroonga indexes this ADR relies on.
- [ADR-008](adr-008-no-redis.md) — the no-extra-infrastructure stance that keeps
  search inside PostgreSQL.
- `internal/persistence/journal/timeline.go` — the message full-text query and the
  literal-term quoting.
- `internal/persistence/roomrepo` — the room-name substring query.
- `internal/persistence/schema.sql` — `CREATE EXTENSION pgroonga` and the two partial
  indexes.

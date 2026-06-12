# ADR-009: Error response format — why dual numeric + string codes with range encoding

- **Status:** Accepted
- **Date:** 2026-06-13
- **Related:** [ADR-002](adr-002-client-protocol.md), [ADR-013](adr-013-business-errors-as-response-values.md)

## Context

Every client-facing failure needs an answer to three different questions,
asked by three different consumers:

1. **A program** branching on the failure: is this retryable? should I
   re-authenticate? which UI state does this map to? Programs want a stable,
   cheap-to-compare identifier.
2. **A human reading logs or a debugger** pasting a response into a terminal:
   what *kind* of failure is this? Humans want a self-describing name, not a
   number to look up.
3. **An end user** seeing a message: what happened, in words? Display copy
   changes with product language and must not be load-bearing for either of
   the above.

HTTP status codes alone cannot serve consumer 1: a single status fans out to
several distinct conditions (one 401 covers missing, expired, and invalid
tokens — the client reaction differs), and grain-level business failures
([ADR-013](adr-013-business-errors-as-response-values.md)) are finer-grained
than the status vocabulary.

## Decision

**Every client-facing error is one JSON envelope carrying three fields: a
range-encoded numeric `code`, a SCREAMING_SNAKE `status` string, and a
human-readable `message`.**

```json
{"error": {"code": 2001, "status": "ROOM_NOT_MEMBER", "message": "You are not a member of this room"}}
```

- **`code`** is numeric and range-encoded by category: 1000–1999
  authentication, 2000–2999 room/membership, 3000–3999 rate limiting,
  4000–4999 validation, 5000–5999 system. The range gives programs a
  two-level dispatch — handle a specific code, or handle a whole category
  (`code/1000 == 1` → re-authenticate) — without a lookup table.
- **`status`** is the stable string name (`AUTH_EXPIRED_TOKEN`,
  `ROOM_NOT_FOUND`, …) for log grep-ability and readable client code.
- **`message`** is display/debug copy only. Clients must never branch on it,
  and it must never carry internal detail (actor paths, grain IDs, stack
  traces).
- The taxonomy lives in one place: `internal/gateway/errors.go` defines the
  typed `ErrorCode` constants, and `ErrorCode.Status()` derives the string
  from the code, so a code/status mismatch is unrepresentable at
  construction sites.
- `ErrorCode.HTTPStatus()` is the single source of the code→HTTP mapping
  (2001→403, 2002→409, 2003→404, 1xxx→401, …); every handler routes through
  it so the mapping cannot drift across endpoints. The HTTP status is thus
  *derived from* the taxonomy, never chosen ad hoc.
- `WriteErrorResponse` is the only path that serializes the envelope,
  centralizing the no-internal-leak guarantee.
- The same triple is the grain-boundary error shape: grains return
  `common.ErrorDetail{code, status, message}`
  ([ADR-013](adr-013-business-errors-as-response-values.md)), and the gateway
  translates it 1:1 via `FromProtoErrorDetail`. One taxonomy spans the whole
  system; the WebSocket's error frames carry the same fields.

## Consequences

### Positive

- **Each consumer gets its native handle** — number for programs, name for
  humans, prose for users — without overloading one field with all three
  jobs.
- **Category-level handling is one integer division.** Clients can implement
  "any auth error → login modal" before they know every specific code, and
  new codes within a range inherit sane client behavior.
- **The grain → HTTP translation is mechanical.** Because grains emit the
  same triple, the gateway adds only the HTTP status (derived), so business
  errors defined once in a grain surface consistently on every endpoint.
- **Mismatches are structurally prevented** — status derives from code, HTTP
  derives from code, and there is one write path.

### Negative

- **Redundancy on the wire.** `code` and `status` encode the same identity
  twice; every response carries both. Accepted as the cost of serving both
  programmatic and human consumers without a lookup table.
- **Ranges are a convention, not a constraint.** Nothing stops a future
  constant from landing in the wrong block except review; the single-file
  taxonomy keeps the review surface small.
- **Not a standard format.** Tooling that understands RFC 9457
  (`application/problem+json`) won't recognize this envelope; consumers of
  this API learn a project-specific (if simple) shape.

### Neutral

- The envelope is uniform across transports: HTTP error bodies and WebSocket
  error frames carry the same fields, so client error handling is written
  once.

## Alternatives considered

### HTTP status codes only

Let 401/403/404/409 speak for themselves. Rejected: too coarse — the three
401 auth conditions demand different client reactions, and grain-level
business failures outnumber usable status codes. Status codes remain in the
design, but as a *derived projection* of the taxonomy, not the taxonomy.

### String status only (no numeric code)

Drop the number, branch on the string. Workable, but loses the range: there
is no cheap "is this any kind of auth failure?" test over strings without
maintaining explicit category lists in every client, and log/metric grouping
by category needs the same lists server-side.

### RFC 9457 `application/problem+json`

The standardized problem-details format (`type` URI, `title`, `detail`,
extensions). Genuinely attractive for interoperability; rejected for this
system because its identity field is a URI (heavier than either consumers
need), the category mechanism would still have to be invented as an
extension, and the project's grain boundary already speaks the
code/status/message triple ([ADR-013](adr-013-business-errors-as-response-values.md))
— a second vocabulary at the edge would mean translating between two error
languages instead of zero.

## References

- [ADR-013](adr-013-business-errors-as-response-values.md) — the same triple
  at the grain boundary; this ADR is its client-facing projection.
- [ADR-002](adr-002-client-protocol.md) — the protocol the envelope rides on.
- `internal/gateway/errors.go` — taxonomy, `Status()`/`HTTPStatus()`
  derivations, envelope types, and the single write path.
- `proto/common/common.proto` — the shared `ErrorDetail` carrying the triple
  between grains and gateway.

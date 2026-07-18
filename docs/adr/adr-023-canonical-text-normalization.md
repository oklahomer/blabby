# ADR-023: Canonical text normalization — NFC and LF at the domain boundary

- **Status:** Accepted
- **Date:** 2026-07-18
- **Related:** [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md), [ADR-020](adr-020-pgroonga-search-stack.md)

## Context

User-supplied text enters the system through several boundaries: room names and
chat messages over REST, search fragments as query parameters, handles, mail
addresses, and passwords at registration and login. Two properties of that input
make byte-wise handling unreliable.

First, the same visible string has more than one Unicode encoding. macOS IMEs and
file dialogs commonly emit decomposed sequences (NFD: `か` followed by combining
U+3099), while Windows and Linux input methods produce the precomposed form
(NFC: `が`). Room-name uniqueness, password verification, and PGroonga matching
([ADR-020](adr-020-pgroonga-search-stack.md)) all compare bytes, so two visually
identical strings silently diverge: a room created from one platform is not found
from another, and a password typed on the keyboard that registered it can fail to
verify elsewhere.

Second, clients disagree on line endings. Browsers submit textarea content as
CRLF through form encoding but as LF through `fetch`; other clients vary again.
Message text with mixed line endings complicates byte-length caps, full-text
indexing, and rendering on every consumer.

## Decision

**All user-supplied text is canonicalized inside the domain constructors that
parse it: Unicode normalization to NFC for every text value object; chat message
text additionally maps CRLF and lone CR to LF and rejects control characters
other than `\n` and `\t`; passwords are NFC-normalized — never trimmed — before
both hashing and verification.**

- **Canonicalization lives in the constructors, not the handlers.** The text
  value objects (`RoomName`, `Handle`, `MailAddress`, `MessageText`, and the
  search-fragment types) extend the parse-at-boundary pattern of
  [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md): once a
  value exists, it is proven canonical. Normalizing per endpoint is the classic
  mismatch vulnerability — one path normalizes, another does not, and the
  difference yields duplicate accounts or spoofed lookups. A constructor cannot
  be skipped.
- **NFC, not NFKC.** Canonical composition only re-encodes what the user typed;
  it never rewrites content. Compatibility folding (NFKC) would turn full-width
  CJK punctuation, `①`, or `ﬁ` into different characters — appropriate for
  match keys, destructive for chat text. The only identifier, the handle, is
  ASCII-restricted, so no NFKC surface exists.
- **Pipeline order, caps on the canonical form.** Each constructor validates
  UTF-8, canonicalizes (newlines where applicable, then NFC), trims, and only
  then applies emptiness, byte-cap, and character checks. NFC can lengthen a
  UTF-8 string (composition-excluded code points such as U+0958 decompose), so
  a cap checked before normalization would admit values the store never sees.
- **Message text gets the newline and control rules.** CRLF and lone CR map to
  LF, giving one stored shape regardless of client; control characters other
  than `\n` and `\t` (NUL, escape sequences, bidi overrides) are rejected,
  matching the strictness room names already apply. Room names keep rejecting
  newlines outright — a name is single-line by definition.
- **Search fragments canonicalize identically.** The fragment types apply the
  same NFC and newline mapping, so PGroonga queries compare like with like
  against stored text.
- **Mail addresses accept an application-defined ASCII subset of RFC 5321.**
  A dot-string local part of at most 64 bytes and a domain of
  letter-digit-hyphen labels, parsed explicitly at the boundary. The RFC's
  quoted local parts and address literals, and the SMTPUTF8 forms of
  RFC 6531/6532 (Unicode local parts, U-label domains), are intentionally
  unsupported; ASCII IDNA A-labels (`xn--…`) remain valid, being ordinary
  LDH labels. The whole address is lowercased — an application policy shared
  with major providers rather than an RFC 5321 rule (§2.4 keeps local parts
  case-sensitive in principle) — and the ASCII grammar makes that lowering
  exact, so no Unicode case-folding subtlety can reach the stored login key.
- **Passwords adopt the NFC rule drawn from RFC 8265's OpaqueString profile.**
  NFC before hashing and before verification, no trimming — whitespace inside
  or around a password is significant. This is deliberately not the full
  profile: its non-ASCII space mapping and code-point restrictions are not
  applied.
- **Machine-generated values are exempt.** Opaque tokens are compared byte-exact
  and never normalized.

## Consequences

### Positive

- **Cross-platform equality.** The same visible text produces the same bytes, so
  room-name uniqueness, message search, and password verification behave
  identically regardless of the client OS or input method.
- **One stored form.** The journal, the PGroonga indexes, and every fan-out
  carry canonical text; consumers render without newline special-casing, and
  the terminal client's CR sanitization becomes defense in depth rather than a
  load-bearing fix.
- **The type carries the proof.** Code holding a `RoomName` or `MessageText`
  needs no further normalization or re-validation, in the same way it already
  trusts parsed identifiers.

### Negative

- **Stored text is not byte-identical to what was sent.** Acceptable while the
  server handles plaintext; a future design that signs or end-to-end-encrypts
  message content could not rewrite it and would need to renegotiate this
  decision for that path.
- **New text inputs must adopt the pattern.** A future field parsed as a raw
  `string` would silently sit outside the canonical space; reviews need to
  route any new user text through a value object.

### Neutral

- **Confusables remain.** Normalization is not homoglyph folding: Cyrillic `а`
  and Latin `a` stay distinct. The login identifiers — handles and mail
  addresses — are ASCII-only, so identifier spoofing is structurally excluded;
  look-alike text in messages and display names is possible, as in most chat
  products.
- **Idempotent by construction.** NFC and the newline mapping are fixed points
  on their own output, so re-parsing stored values during hydration or replay
  is a no-op.
- **Password hashes bind to the canonical form.** Every credential write and
  every verification normalize identically before hashing, so a hash derived
  from non-canonical bytes cannot exist in this system; a credential store
  populated outside this pipeline would not verify.
- **The account store binds to the accepted grammar.** A row holding a mail
  address outside the ASCII subset would be unreachable by login or
  verification; every store is seeded and written through this parser
  (schema.sql, `make db-reset`), so no such row exists.

## Alternatives considered

### NFKC everywhere

Folds compatibility variants and thereby maximizes match rates, which is why
nickname-comparison profiles use it. But it rewrites content — full-width
punctuation a CJK author typed deliberately, superscripts, ligatures — and
blabby has no non-ASCII match keys that would benefit. Rejected for content;
unnecessary for identifiers.

### Normalize in the HTTP and WebSocket handlers

Reaches the same bytes when done exhaustively, but every new endpoint must
remember, and a single missed path splits the canonical space — the very bug
normalization exists to prevent. Constructor placement makes the guarantee
structural instead of procedural.

### Store as sent, normalize at comparison time

Preserves byte fidelity, but then uniqueness checks, index expressions, search
queries, and every client comparison must normalize forever, and one missed
site reintroduces the divergence. The PGroonga indexes would also index
non-canonical bytes, pushing normalization into SQL expressions on both sides.

### Reject non-NFC input instead of converting

Shifts the burden onto every client and IME; macOS users would see validation
errors for ordinarily typed text. Conversion is lossless under canonical
equivalence, so rejection adds friction without adding safety.

## References

- [ADR-014](adr-014-domain-identifier-types-and-boundary-parsing.md) — the
  parse-at-boundary pattern these value objects extend.
- [ADR-020](adr-020-pgroonga-search-stack.md) — the search stack whose indexes
  and queries rely on one canonical form.
- [UAX #15: Unicode Normalization Forms](https://unicode.org/reports/tr15/) —
  NFC/NFD/NFKC/NFKD and their equivalence guarantees.
- [RFC 8264](https://www.rfc-editor.org/rfc/rfc8264),
  [RFC 8265](https://www.rfc-editor.org/rfc/rfc8265),
  [RFC 8266](https://www.rfc-editor.org/rfc/rfc8266) — the PRECIS framework:
  NFC for usernames and passwords (OpaqueString), NFKC for nicknames.
- [W3C Character Model for the World Wide Web](https://www.w3.org/TR/charmod-norm/)
  — the NFC recommendation for web content.
- [WHATWG on newline normalization in form submission](https://blog.whatwg.org/newline-normalizations-in-form-submission)
  — why servers see mixed CRLF/LF.

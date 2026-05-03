# ADR-013: Business errors as response values, carried by a shared `ErrorDetail`

- **Status:** Accepted
- **Date:** 2026-05-03
- **Related:** [ADR-009](adr-009-error-response-format.md), [ADR-011](adr-011-cross-boundary-pid-propagation.md)

## Context

Every grain RPC in this system can fail in two distinct ways:

1. **Transport failures** — the request never reaches the grain, the response never makes it back, the actor system is unreachable, the call times out, the cluster doesn't know where the grain lives. These are framework concerns; the application has nothing meaningful to say about them beyond "retry / give up / report unhealthy."
2. **Business failures** — the request reached the grain, the grain understood it, and the answer is "no" for a reason the caller cares about: the user isn't a member of the room, the message text is empty, the requested room doesn't exist. These are *expected* outcomes of the domain model.

The textbook gRPC idiom collapses both into the framework's status model: every RPC returns either a successful response message OR a `google.rpc.Status` (numeric code from a fixed taxonomy + message + optional typed `details`). Response messages contain only success-path data. Failures travel out-of-band through the framework's error channel, where interceptors, retry policies, dashboards, and load balancers all understand them natively.

That model is a strong fit when:

- The caller treats most failures as exceptional (rare, retry-worthy, or fatal).
- The framework's error transport preserves the structured details the caller needs to act on the failure.
- Observability tooling consumes status codes directly.

It is a weaker fit in this system, for two specific reasons:

**The grain client's transport doesn't carry typed details across the cluster boundary cleanly.** protoactor-go's generated grain client returns `(*Response, error)`. The Go `error` channel is reserved for transport-level failures (network, timeout, dead actor). Application-level errors *can* be carried by returning a non-nil `error` from the grain handler — the cluster wraps it as a `GrainErrorResponse` and the client sees it as a Go error — but the wrapping is stringly-typed. There is no equivalent of gRPC's `google.rpc.Status.details` for round-tripping typed protobuf error payloads through the protoactor cluster client. Recovering structured fields (numeric code, status enum, human message) on the client would require unmarshaling the error string by convention.

**Business failures here are *not* exceptional.** "You're not a member of this room" is a perfectly normal answer to a Leave command. "The message text is empty" is a perfectly normal validation outcome. Modeling these as Go errors — values that propagate up the stack until something handles them — overstates how unusual they are and pushes callers toward defensive `if err != nil` patterns where what they actually want is a structured branch on the response.

We also have a non-gRPC consumer to serve: an HTTP gateway translates grain responses into JSON for WebSocket and REST clients. A JSON consumer expects a structured error envelope in the response body, not a side-channel status code that the JSON marshaller has to fish out of a Go error.

## Decision

**Business failures are encoded as values inside the response message, carried by a single shared `ErrorDetail` proto. Go `error` (and the cluster's `GrainErrorResponse`) is reserved for transport-level failures.**

Concretely:

- Every grain response message that can carry a business failure has the shape:
  ```protobuf
  message FooResponse {
    bool success = 1;
    common.ErrorDetail error = 2;  // nil when success=true
    // ... success-path fields ...
  }
  ```
- `ErrorDetail` is defined once in `proto/common/common.proto` and imported by every proto file that needs it. Both grains and any future grain reference the same Go type (`commonpb.ErrorDetail`).
  ```protobuf
  message ErrorDetail {
    int32 code = 1;     // numeric code from the project's error taxonomy
    string status = 2;  // SCREAMING_SNAKE enum-ish status string
    string message = 3; // short human-readable description
  }
  ```
- The contract is **mutually exclusive**: when `success = true`, `error` is nil and the success-path fields are populated. When `success = false`, `error` is non-nil and the success-path fields are zero-valued. Callers check `success` first; the response shape makes "succeeded with an error attached" unrepresentable in practice.
- A grain handler returns a non-nil Go `error` only when the framework needs to know the call failed at the transport layer (e.g. a downstream cluster call that itself returned a transport error and cannot be meaningfully translated to a business error). Returning Go errors for business outcomes is forbidden.
- The numeric `code` and the `status` string come from the project's error taxonomy (see [ADR-009](adr-009-error-response-format.md)). The HTTP gateway maps the same taxonomy to HTTP status codes for REST clients and to the same JSON envelope for WebSocket clients.

## Consequences

### Positive

- **One pathway per failure class.** Transport failures travel as Go errors; business failures travel as response fields. Callers don't have to check both channels for the same condition.
- **Structured details survive the cluster boundary.** The numeric code, status enum, and human message are first-class proto fields. No string-parsing of error messages on the client.
- **The HTTP gateway has a clean translation.** JSON clients get a structured error envelope by serializing the response directly; no Go-error-to-JSON adapter is needed.
- **One Go type for errors across grains.** `commonpb.ErrorDetail` is shared, so middleware (logging, metrics) can pattern-match on a single type to extract `code` and `status`.
- **Failures are visibly part of the API surface.** A reader of `user.proto` sees that `JoinRoom` can fail and how, without consulting a separate error catalogue or framework documentation.

### Negative

- **gRPC tooling doesn't see business failures as "errors."** Interceptors that count failures by gRPC status code, retry policies that act on `UNAVAILABLE`/`DEADLINE_EXCEEDED`, and dashboards that group by status code all see only `OK` for a business failure that returned `success=false`. Observability for business failures has to be built separately, on `success=false` and `error.code` instead of on framework status codes.
- **Caller discipline is required.** A caller that forgets to check `success` and reads success-path fields will see zero values — not a panic, not an error, just silently wrong data. The mutual-exclusion contract has to be enforced by code review, not by the type system. Mitigated by every response carrying `success` as field 1 and by tests asserting both branches.
- **Not what most public gRPC APIs do.** Engineers familiar with the gRPC idiom will need to learn the project's convention. The trade-off is documented here so that's a one-time cost.

### Neutral

- **No effect on transport-level error handling.** Timeouts, dead actors, network failures, and cluster-routing failures still surface as Go errors from the cluster client. The decision affects only how *successful round-trips with bad outcomes* are encoded.

## Alternatives considered

### gRPC status model (`google.rpc.Status` + `details`)

Use the framework's native status model: failed RPCs return a non-nil `error` carrying a numeric code, a message, and (for richness) typed protobuf payloads in `details`.

Rejected for this system because (a) protoactor-go's cluster client serializes the error channel as a Go `error`, not as a typed `google.rpc.Status` with deserializable `details`; recovering structured fields on the client requires hand-rolled string parsing or out-of-band conventions, and (b) modeling routine domain outcomes as exceptions encourages defensive caller code where a structured branch is more honest about the API.

### Inline scattered error fields (`success bool` + `error_code int32` + `error_status string` + `error_message string`)

Skip the nested message and put the three error fields directly on the response. Same information, fewer types.

Rejected because the four-field response loses the structural signal that the error fields are a unit. Easy to populate `error_code` and forget `error_status`, easy to read a success-path field (e.g. `Timestamp`) without checking `Success` first because the field looks like just another piece of data. Nesting under a single nullable `ErrorDetail` makes "success or error, never both" the obvious shape and gives reviewers one symbol to grep for.

### Sentinel Go errors (`var ErrNotMember = errors.New("...")`)

Define typed error sentinels per failure mode and rely on Go's `errors.Is`/`errors.As` machinery. Plays nicely with idiomatic Go but doesn't solve the cross-cluster problem: protoactor-go's transport doesn't preserve sentinel identity across nodes. The client sees a generic `*GrainErrorResponse` wrapping a string, not the original sentinel. Rejected.

## Scope of applicability

This ADR governs grain RPC response shapes within blabby. It does **not** govern:

- **Transport-level error handling** — Go errors from the cluster client continue to flow as Go errors through application code. Don't wrap them in `ErrorDetail`.
- **Internal package boundaries** — code inside a single grain (e.g., `internal/grain/user/state.go`'s helpers) uses regular Go errors per Go idiom. The decision applies at the grain RPC boundary, not at every function.
- **External APIs** — the HTTP gateway translates `ErrorDetail` to HTTP status codes and a JSON envelope per [ADR-009](adr-009-error-response-format.md). The gateway's external contract is its own ADR's concern.

## References

- [ADR-009](adr-009-error-response-format.md) — Error response format. Defines the numeric code taxonomy and status string vocabulary that `ErrorDetail.code` / `ErrorDetail.status` draw from.
- [ADR-011](adr-011-cross-boundary-pid-propagation.md) — Cross-boundary PID propagation. Same general principle (carry typed data in the proto body rather than relying on framework-implicit context) applied to PIDs; ADR-013 applies it to error context.
- `proto/common/common.proto` — definition of the shared `ErrorDetail` message.

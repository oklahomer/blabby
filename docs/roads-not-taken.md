# Roads not taken

Proto.Actor offers more than blabby uses, and the choices are as
instructive as the code. Each entry below follows one shape: what the
feature offers, what blabby needed, what the code does instead, and the
conditions under which the feature is the right reach. None of these are
verdicts on the feature; they are records of a fit test against one
system's requirements.

## Cluster pub-sub vs the fan-out child

[Cluster pub-sub](https://proto.actor/docs/ProtoActor/cluster/pub-sub)
gives you named topics with cluster-wide subscribers: publish once, the
topic actor delivers everywhere. A topic per room is the textbook shape for
chat fan-out, and the Go module ships the feature (the official docs mark
it experimental).

blabby needed two properties that pull away from topics. Delivery had to be
tied to a *membership snapshot* taken at the moment a message commits, so
that who-receives-what follows the database's answer, not a subscription
set with its own lifecycle
([ADR-005](adr/adr-005-unconditional-fan-out.md)). And the sender's own
echo had to be deadlock-free, which the
[fan-out child](reentrancy-and-timers.md) solves while keeping delivery
under the Room grain's control
([ADR-015](adr/adr-015-command-query-vs-notification.md)).

Reach for pub-sub when topics outlive any one publisher, subscriber sets
are open-ended, and the publisher genuinely should not know who is
listening.

## Routers vs cluster placement

[Routers](https://proto.actor/docs/ProtoActor/routers) distribute messages
across a routee set: round-robin and random pools for interchangeable
workers, broadcast groups, consistent-hash routing for sticky keys.

blabby's fan-out recipients are dynamic grain identities resolved per
message from room membership, not a fixed routee set, so pool and group
routers do not model the problem. And the consistent-hash use case is
already covered one layer down: disthash *is* consistent-hash placement,
applied cluster-wide to every grain identity (the
[cluster-bootstrap tour](cluster-bootstrap.md)).

Reach for a router when a stage needs N interchangeable, stateless workers
behind one PID; a message-indexing pipeline would be the natural first use
here.

## Proto.Persistence vs Postgres repositories

[Proto.Persistence](https://proto.actor/docs/ProtoActor/persistence) gives
actors event-sourced state: persist events as they apply, snapshot
periodically, replay on activation.

blabby's grain state is deliberately not an event-sourced aggregate. It is
a cache over PostgreSQL
([ADR-007](adr/adr-007-database-authoritative-persistence.md)): rows are
the source of truth, activations hydrate with plain queries, and features
like the room timeline and full-text search
([ADR-020](adr/adr-020-pgroonga-search-stack.md)) want ad-hoc SQL over the
same data, which an event log would make a second system
([ADR-008](adr/adr-008-no-redis.md) records the same one-store instinct).

Reach for event sourcing when the history *is* the domain — audit trails,
temporal queries, rebuilding projections — and your read paths can live
with projections instead of ad-hoc queries.

## Receive-timeout vs the one-shot auth timer

[Receive-timeout](https://proto.actor/docs/ProtoActor/receive-timeout) is
the runtime's idle detector: the clock resets on every message. That reset
is exactly why the connection's five-second auth deadline does not use it —
a hard wall clock must not be extended by unrelated traffic. The full
trade-off lives in the
[reentrancy-and-timers tour](reentrancy-and-timers.md); the place
receive-timeout fits perfectly is grain passivation, where the generated
code already uses it (the
[lifecycle-and-passivation tour](lifecycle-and-passivation.md)).

## Bounded mailboxes vs backpressure at the socket

[Mailboxes](https://proto.actor/docs/ProtoActor/mailboxes) can cap queued
user messages before memory grows; in the Go module, `Bounded` applies
backpressure by blocking the producer when the buffer is full, and
`BoundedDropping` discards the oldest queued message to make room. Neither
produces a dead letter — those stay scoped to messages sent to PIDs that no
longer exist (the [observability tour](observability.md)). blabby leaves
every mailbox at the unbounded default and bounds the WebSocket write path
instead: a fixed-capacity outbound channel whose overflow is treated as a
failed connection (`connection.write.backpressure`, and the supervisor
stops the actor). The
[backpressure](https://proto.actor/docs/ProtoActor/backpressure) question
is "where is the slow consumer?", and here the answer is the socket, so the
bound lives at the socket. A bounded mailbox becomes the right tool when
the actor itself is the bottleneck and callers can tolerate shedding.

## Also in the toolbox, currently unused

One line each, so the surface is known even where blabby has no lesson to
offer yet: sender and spawn middleware (receiver-side is well covered),
context decorators, behavior stacking (`BecomeStacked`), stashing, typed
and untyped streams, the `testkit` probe helpers, `plugin.PassivationPlugin`
(hand-written actors get passivation this way; blabby's generated grains
have it built in), gRPC compression on the remote, and per-kind member
strategies for custom placement.

## Try it

This tour's hands-on step is a reading exercise: pick the entry whose
trade-off you find least convincing, open the ADR it cites, and check the
Consequences section for the cost blabby accepted. Every one of these
choices bought something and paid something, and the ADRs are where the
prices are written down.

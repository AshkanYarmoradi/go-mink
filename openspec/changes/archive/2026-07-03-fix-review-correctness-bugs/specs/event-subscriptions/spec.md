## ADDED Requirements

### Requirement: Position-based delivery never skips an out-of-order-committed event
The PostgreSQL shared load-from-position mechanism SHALL advance its cursor/checkpoint only
across a contiguous, stable prefix of `global_position`, bounded by a safe watermark below
which no in-flight transaction can still commit a lower position — covering `SubscribeAll`,
`SubscribeCategory`, async projections (`loadEventsFromPosition`, which also checkpoints),
and the saga manager's event loop. The watermark SHALL be enforced
at the adapter's load-from-position query (returning nothing above it) and SHALL NOT
require a DB-schema change (it uses transaction-snapshot / `xmin` visibility or
gap-detection with a staleness timeout). An event whose transaction is assigned a lower
`global_position` but commits after a higher-positioned transaction SHALL still be
delivered to every consumer. At-least-once delivery (possible duplicates on restart) is
preserved; at-most-once loss is eliminated.

#### Scenario: Lower position that commits late is still delivered
- **WHEN** two concurrent appends receive positions 5 and 6, position 6 commits first and is polled, then position 5 commits
- **THEN** the consumer still receives the event at position 5 because the cursor did not advance past the unstable prefix

#### Scenario: The fix covers projections and sagas, not just subscriptions
- **WHEN** an async projection or saga polls the shared load-from-position mechanism under out-of-order commits
- **THEN** it never checkpoints/advances past an event whose lower-positioned transaction has not yet committed, so no projection update or saga trigger is silently lost

#### Scenario: A rolled-back transaction's permanent gap does not stall delivery
- **WHEN** a transaction consumes a `global_position` from the sequence but then rolls back (leaving a permanent gap)
- **THEN** delivery does not wait forever on the missing position — once no in-flight transaction could fill it, the watermark advances past it

### Requirement: In-memory subscribe snapshots history and registers atomically
The in-memory adapter's `SubscribeAll` SHALL register the subscriber and capture its
starting position within the same critical section that `Append` uses, so an `Append`
concurrent with subscription setup is delivered exactly once — via history or the live
channel — and never lost in the gap between snapshot and registration.

#### Scenario: Append during subscribe setup is not lost
- **WHEN** an `Append` occurs while `SubscribeAll` is setting up its historical snapshot
- **THEN** the event is delivered to the subscriber (history or live) and never silently dropped

### Requirement: Category matching escapes LIKE metacharacters
`SubscribeCategory` SHALL escape `%` and `_` in the category prefix before appending the
`-%` wildcard, so a category name containing those characters matches only its own
streams.

#### Scenario: Category with an underscore does not over-match
- **WHEN** subscribing to a category whose name contains `_`
- **THEN** only streams in that exact category are delivered, not `LIKE`-adjacent categories

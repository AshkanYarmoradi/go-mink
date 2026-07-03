## ADDED Requirements

### Requirement: Subscriptions never skip an out-of-order-committed event
PostgreSQL `SubscribeAll` and `SubscribeCategory` SHALL advance their cursor only across
a contiguous, stable prefix of `global_position`, bounded by a safe watermark below
which no in-flight transaction can still commit a lower position. An event whose
transaction is assigned a lower `global_position` but commits after a higher-positioned
transaction SHALL still be delivered. At-least-once delivery (possible duplicates on
restart) is preserved; at-most-once loss is eliminated.

#### Scenario: Lower position that commits late is still delivered
- **WHEN** two concurrent appends receive positions 5 and 6, position 6 commits first and is polled, then position 5 commits
- **THEN** the subscriber still receives the event at position 5 because the cursor did not advance past the unstable prefix

#### Scenario: No committed event is skipped under interleaved commits
- **WHEN** many writers append and commit in an order different from their assigned positions
- **THEN** every committed event is delivered at least once and none is permanently skipped

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

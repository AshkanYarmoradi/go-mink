# event-subscriptions Specification

## Purpose
TBD - created by archiving change fix-review-correctness-bugs. Update Purpose after archive.
## Requirements
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

### Requirement: Filtered load-from-position read
go-mink SHALL provide a filtered variant of the load-from-position read that pushes indexed predicates into the storage query, so a consumer scanning the global feed for a narrow slice touches only matching rows instead of loading every event and filtering in application code. It SHALL be exposed as `EventStore.LoadEventsFromPositionFiltered(ctx, fromPosition, limit, filter)` over a new **optional** `FilteredFeedAdapter` interface, mirroring how `LoadEventsFromPosition` sits over the optional `SubscriptionAdapter`; when the underlying adapter does not implement `FilteredFeedAdapter`, the call SHALL return a sentinel `ErrFilteredFeedNotSupported` (as `LoadEventsFromPosition` returns `ErrSubscriptionNotSupported`). Both built-in adapters (postgres, memory) SHALL implement it. The read SHALL preserve the exact contract of `LoadFromPosition`: events ordered by ascending `global_position`, exclusive of `fromPosition`, bounded by `limit`, and — on the PostgreSQL adapter — subject to the **same gapless safe watermark** (`safePositionClause`), so a filtered read inherits the no-skip guarantee and never returns an event above the unstable prefix. An **empty filter SHALL behave identically to `LoadFromPosition`**.

#### Scenario: A narrow type filter reads only matching rows
- **WHEN** `LoadEventsFromPositionFiltered` is called with a filter selecting a single event type over a feed where that type is a small fraction of history
- **THEN** it returns only events of that type, in ascending `global_position` order, up to `limit`, using the `event_type` index rather than loading and discarding non-matching events

#### Scenario: Stream-id and category filters, with metacharacters escaped
- **WHEN** the filter selects a set of exact stream ids, or a stream category whose name contains a `LIKE` metacharacter (`%` or `_`)
- **THEN** only events in those exact streams (or that exact category) are returned, and the category prefix is escaped so it does not over-match `LIKE`-adjacent categories (consistent with `SubscribeCategory`)

#### Scenario: Filter axes AND-compose; sets are OR within an axis
- **WHEN** a filter specifies more than one axis (e.g. event types AND stream ids)
- **THEN** a returned event matches every specified axis, and within a multi-valued axis it matches any member of the set

#### Scenario: An empty filter equals LoadFromPosition
- **WHEN** `LoadEventsFromPositionFiltered` is called with a filter that specifies no axis
- **THEN** it returns exactly what `LoadEventsFromPosition` would for the same `fromPosition` and `limit` (same events, order, exclusivity, and watermark)

#### Scenario: The filtered read respects the gapless watermark
- **WHEN** a lower `global_position` is still uncommitted (in the unstable prefix) while a higher one has committed, and a filtered read spans that range on the PostgreSQL adapter
- **THEN** the filtered read does not return any event above the unstable prefix, exactly as the unfiltered load-from-position mechanism does

#### Scenario: An adapter without the capability is explicit, not silent
- **WHEN** `LoadEventsFromPositionFiltered` is called on a store whose adapter does not implement `FilteredFeedAdapter`
- **THEN** it returns `ErrFilteredFeedNotSupported` rather than falling back to a full scan or panicking

### Requirement: Feed filter is indexed-only (introspection primitive, not a query engine)
The `FeedFilter` SHALL expose only axes backed by an existing event-store index — event type (`idx_events_type`), exact stream id and stream category (`idx_events_stream`). It SHALL NOT provide axes for event `metadata` (including tenant/custom) or event `data`/payload predicates. This keeps the filtered read a bounded, indexed introspection tool — for audit/ops browsers, migration and backfill scanners, and diagnostics — rather than an ad-hoc query engine over the event log. Application queries and any unindexed criterion (tenant scoping, payload fields) remain the job of a purpose-built read-model projection, or first-class `metadata` attribution with a consumer-provided expression index. The read SHALL be documented as introspection / ops / migration tooling, not an application read path.

#### Scenario: The filter surface admits only indexed axes
- **WHEN** a consumer constructs a `FeedFilter`
- **THEN** the only available axes are event type(s), stream id(s), and category — there is no field to filter by metadata, tenant, or payload contents

#### Scenario: Unindexed criteria are directed to a read model
- **WHEN** a consumer needs to filter the feed by a value that is not an indexed axis (e.g. a tenant embedded in metadata, or a field inside the event payload)
- **THEN** the documented guidance is to project a read model (or attribute the value into indexed `metadata` with an expression index), not to extend the feed filter — preserving go-mink's separation of the event log from query models


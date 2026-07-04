## ADDED Requirements

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

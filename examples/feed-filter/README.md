# Filtered feed reads

Read the global event feed filtered by an **indexed** axis — event type, stream id,
or stream category — with `EventStore.LoadEventsFromPositionFiltered`. The predicate
is pushed down to storage, so a narrow read touches only matching rows instead of
loading the whole feed and filtering in Go.

This is an **introspection** primitive: audit browsers, migration/backfill scanners,
and diagnostics. It is *not* an application read path — for queries over unindexed
data (a tenant in metadata, a payload field) or for application reads, project a
[read model](../projections) instead.

## Run

```bash
go run ./examples/feed-filter
```

No database required — it uses the in-memory adapter.

## What to look for

- `FeedFilter{EventTypes: ...}` — every event of a type, across all streams (uses the
  `event_type` index on PostgreSQL).
- `FeedFilter{Category: "order"}` — the whole `order-*` feed, escaped so it can't
  over-match adjacent categories.
- Axes **AND-compose**; a multi-valued axis (e.g. several `EventTypes`) is an OR set.
- An **empty** `FeedFilter` is identical to `LoadEventsFromPosition`.

Results keep the ordering, `limit`, exclusivity, and gapless safe-watermark
guarantees of the unfiltered read. Only the in-memory and PostgreSQL adapters
implement it; another adapter returns `ErrFilteredFeedNotSupported`.

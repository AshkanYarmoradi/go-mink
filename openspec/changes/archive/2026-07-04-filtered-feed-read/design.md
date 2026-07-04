## Context

The load-from-position read path is shared by `SubscribeAll`, `SubscribeCategory`,
async projections, and the saga manager. On postgres it funnels through one builder:

```go
func positionScanQuery(schemaQ, extraWhere, limitParam string) string {
    return `SELECT event_id, stream_id, version, event_type, data, metadata, global_position, timestamp
            FROM ` + schemaQ + `.events
            WHERE global_position > $1` + safePositionClause + extraWhere + `
            ORDER BY global_position ASC
            LIMIT ` + limitParam
}
```

`LoadFromPosition` calls it with an empty `extraWhere`; `loadCategoryEvents` (behind
`SubscribeCategory`) calls it with `" AND stream_id LIKE $2"`. The `extraWhere` seam
therefore **already exists for predicate pushdown** — it is just not exposed as a
general, positional (non-subscription) read. Consumers that want "the feed, but only
these types/streams" (audit browsers, migration scanners, the `mink` CLI, huisscan's
ops panel) have to `LoadFromPosition` the whole feed and filter in Go: O(total
history) per query, plus transfer + deserialize of discarded rows.

This is the read-side analogue of EF2 (`bounded-retention-scan`): the same feed, the
same "don't touch what you don't need" fix, one layer up.

## Goals / Non-Goals

**Goals**
- A first-class, bounded, filtered read of the global feed on indexed axes (type,
  stream, category), reusing the existing query builder, watermark, and scanner.
- Inherit the gapless no-skip guarantee for free (same `safePositionClause`).
- Additive, optional, capability-detected — zero overhead and zero behavior change
  when unused; no schema/index change.

**Non-Goals**
- Filtering by `metadata`/tenant or `data`/payload — unindexed; belongs in a read
  model (see D2). This is the guardrail, not an omission.
- A filtered *subscription* (`SubscribeAll` variant) — this is a positional read only.
- Refactoring `loadCategoryEvents` onto the new primitive — kinship noted, code left
  untouched to keep the change additive.

## Decisions

### D1: Expose via a new **optional** `FilteredFeedAdapter`, not by extending `SubscriptionAdapter`
`LoadEventsFromPosition` already sits over an optional adapter capability: it type-
asserts `s.adapter.(adapters.SubscriptionAdapter)` and returns
`ErrSubscriptionNotSupported` when absent. The filtered read follows that pattern
exactly with a sibling interface:

```go
// adapters/adapter.go
type FeedFilter struct {
    EventTypes []string // event_type ∈ set (idx_events_type)
    StreamIDs  []string // exact stream_id ∈ set (idx_events_stream)
    Category   string   // stream category prefix, escaped (idx_events_stream)
}

type FilteredFeedAdapter interface {
    LoadFromPositionFiltered(ctx context.Context, fromPosition uint64, limit int, filter FeedFilter) ([]StoredEvent, error)
}
```

- *Alternative — add `LoadFromPositionFiltered` to `SubscriptionAdapter`:* rejected.
  It breaks every third-party adapter at compile time (a v1 major-surface break) for a
  capability not all backends can serve.
- *Alternative — variadic option on `LoadFromPosition`:* rejected. Overloads a hot,
  well-understood signature and still needs adapter-side support detection.

The optional interface is non-breaking, mirrors the proven `SubscriptionAdapter`
shape, and lets each adapter opt in. Both built-ins opt in.

### D2: **Indexed-only** filter axes — the crux, and why the restriction is the feature
The filter carries only columns the event table already indexes:
`event_type` (`idx_events_type`), `stream_id`/category (`idx_events_stream`). It
deliberately has **no** `metadata`/tenant or `data`/payload axis.

An event log is not a query store. go-mink's answer for "events matching an arbitrary
predicate" is a projected read model; its answer for tenant scoping is first-class
`metadata` attribution plus a consumer-owned expression index. If the feed filter
grew a `data LIKE` or `metadata->>` axis, it would (a) invite unindexed full scans
wearing a "filter" label, and (b) tempt consumers to query the log where they should
project. Restricting to indexed axes keeps the primitive honestly bounded and keeps
the CQRS boundary intact. Consumers with unindexed needs (huisscan's best-effort
workspace-in-payload match) keep that logic in their own layer — which is the correct
split of "go-mink owns the indexed primitive, the consumer owns the policy."

### D3: Reuse `positionScanQuery` / `scanEvents` / `safePositionClause` — assemble, don't re-implement
The postgres impl is a generalization of `loadCategoryEvents`: build `extraWhere` from
the filter and call the existing `positionScanQuery`, then `scanEvents` the result.

```go
// extraWhere placeholders start at $2 (position is $1); limit is the last param.
// e.g. filter{EventTypes:[a,b], Category:c} →
//   " AND event_type IN ($2,$3) AND stream_id LIKE $4"   , args: [from, a, b, c-%, limit]
```

Because the query still flows through `positionScanQuery`, the `safePositionClause`
watermark is applied **unchanged** — the filtered read inherits the same gapless,
no-skip visibility the unfiltered readers have (the `event-subscriptions` watermark
requirement covers it for free). Category prefixes are escaped with the existing
`escapeLikePattern` + `-%`, identical to `SubscribeCategory`.

### D4: Filter semantics — AND across axes, OR within, empty ≡ unfiltered
Axes AND-compose; a multi-valued axis is an `IN`/set membership (OR). An axis left
empty contributes no clause, so an all-empty `FeedFilter` produces the exact query
`LoadFromPosition` runs today (parity is a tested scenario). Slice-valued axes are a
strict superset of single-value filtering — a consumer wanting one type passes a
one-element slice.

### D5: `IN`-list with explicit placeholders, not driver array binding
`extraWhere` renders `event_type IN ($2,$3,…)` / `stream_id IN (…)` with one
placeholder per value (as huisscan's local builder does), rather than
`= ANY($2::text[])`. This avoids depending on a specific driver's array codec and
keeps the SQL uniform with the rest of the adapter. (A future `ANY(array)` form is a
safe internal swap if desired; it changes no behavior.) A sane cap on set size can be
documented; over-large sets are a caller smell better served by a category or a read
model.

### D6: Memory adapter parity
The memory adapter implements the same read as its ordered position scan plus an
in-slice predicate mirroring the SQL (`type ∈ set`, `stream ∈ set`, category prefix
match). This gives tests and non-postgres consumers identical filtering semantics and
lets the whole feature be covered without a database.

### D7: Names and error, mirroring existing surface
- Adapter method: `LoadFromPositionFiltered` (parallels `LoadFromPosition`).
- Store wrapper: `LoadEventsFromPositionFiltered` (parallels `LoadEventsFromPosition`).
- Interface: `FilteredFeedAdapter`; filter type: `FeedFilter`.
- Sentinel: `ErrFilteredFeedNotSupported` (parallels `ErrSubscriptionNotSupported`),
  returned by the wrapper when the adapter lacks the capability.

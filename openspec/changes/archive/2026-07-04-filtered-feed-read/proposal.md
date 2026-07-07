## Why

go-mink's load-from-position family — `EventStore.LoadEventsFromPosition` over the
adapter's `LoadFromPosition` — reads the **whole global feed** with no predicate
pushdown. `SubscribeCategory` is the one exception: it filters the same scan by a
single axis (stream category, `stream_id LIKE cat-%`). Anything narrower — "every
`OrderPlaced` since position N", "these three streams", an admin audit browser
filtered by event type — has **no first-class read path**. The consumer must load
every event and filter in application code, so a query that logically matches a
handful of rows still pays an **O(total history)** scan that grows for the life of
the store, plus per-row (de)serialization and transfer of events it immediately
discards.

This was surfaced in a production operations-panel review as efficiency finding
**EF1** (sibling of EF2, the `bounded-retention-scan` change): the ops audit panel
filters the event feed by type / stream / workspace, and each filtered list or CSV
export walked up to 200k events in Go per request to return ~20 matching rows.
the consumer closed EF1 locally by reaching into the physical `events` table with a
hand-rolled filtered query — **duplicating machinery go-mink already owns** (the
`safePositionClause` gapless watermark, `escapeLikePattern`, `scanEvents`, and the
`positionScanQuery(schemaQ, extraWhere, limitParam)` builder whose `extraWhere` slot
exists for exactly this). That duplication is the signal: the primitive belongs in
go-mink.

There is a **latent second consumer inside go-mink itself** — the `mink` CLI's
stream-inspection / diagnostics surface. A `mink events --type=… --since=…` inspector
wants precisely this read. And it closes a standing inconsistency: go-mink markets
"a perfect audit log — who changed what, when, and why" as a headline feature, yet
offers no first-class way to *query* that log **by type**.

The guardrail that makes this safe (and keeps it from becoming an anti-pattern) is
**indexed-only scope**: the filter covers only columns go-mink already indexes
(`event_type`, `stream_id` / category). Arbitrary metadata / payload predicates stay
out — those belong in a read-model projection, which is go-mink's answer for
application queries. This adds an *introspection* primitive without inviting people
to query the log where they should be projecting.

## What Changes

### Required

- **Filtered load-from-position read.** A new `EventStore.LoadEventsFromPositionFiltered(ctx, fromPosition, limit, filter)`
  over a new **optional** `FilteredFeedAdapter` interface — mirroring how
  `LoadEventsFromPosition` sits over the optional `SubscriptionAdapter`. The filter
  (`FeedFilter`) carries indexed axes only:
  - `EventTypes []string` → `event_type` ∈ set (uses `idx_events_type`);
  - `StreamIDs []string` → exact `stream_id` ∈ set (uses `idx_events_stream`);
  - `Category string` → stream category prefix, escaped exactly like
    `SubscribeCategory` (uses `idx_events_stream`).

  Axes AND-compose; within an axis the set is OR (`IN` / `= ANY`). An **empty filter
  behaves identically to `LoadFromPosition`**. Results keep the existing contract:
  ascending `global_position`, exclusive of `fromPosition`, bounded by `limit`, and —
  on postgres — under the **same gapless `safePositionClause` watermark**, so a
  filtered read inherits the no-skip guarantee.
- **Both built-in adapters implement it** — postgres (via `positionScanQuery`'s
  `extraWhere` + `scanEvents`) and memory (in-slice predicate over the same ordered
  history). An adapter that does not implement `FilteredFeedAdapter` yields a sentinel
  `ErrFilteredFeedNotSupported` (mirroring `ErrSubscriptionNotSupported`).

### Good-to-have

- **`mink` CLI inspector** — a verb (e.g. `mink events --type= --stream= --category=
  --from= --limit=`) built on the new read, consistent with the existing `mink stream`
  / `mink projection` style. Left as a follow-up so this change stays a tight,
  reusable primitive; called out here because it is the in-repo second consumer that
  justifies the primitive's generality.

### Non-Goals

- **No metadata or payload filtering.** No `tenant`, `custom`, or `data`-substring
  axis. Tenant/workspace scoping is a *consumer* concern: attribute it in `metadata`
  and add an expression index, or project a read model. Keeping the primitive
  indexed-only is deliberate — it prevents an ad-hoc event-log query engine and keeps
  go-mink's "project read models for queries" model intact. (the consumer therefore keeps
  its best-effort `data::text LIKE` workspace fallback local; see Impact.)
- **No change to existing readers.** `LoadFromPosition`, `SubscribeAll`,
  `SubscribeCategory`, projections, and the saga loop are untouched. The new interface
  is additive and optional — zero overhead and zero behavior change when unused.
- **No new index or schema.** The filter targets only already-created indexes; no DDL
  change. (A future metadata/tenant index is a separate, explicitly opt-in concern.)
- **No live/subscribe filtered variant.** This is a bounded, positional *read*, not a
  filtered `SubscribeAll`. A streaming filtered subscription can follow if a consumer
  needs it.

## Capabilities

### Modified Capabilities

- `event-subscriptions`: adds a filtered variant of the load-from-position read
  (indexed axes: event type, stream id, category), implemented in both adapters,
  preserving the existing ordering, `limit`, exclusivity, and gapless-watermark
  guarantees. The existing readers and the watermark requirement are unchanged; the
  new read reuses the same safe-position mechanism.

## Impact

- **`adapters/adapter.go`**: new `FeedFilter` struct (indexed axes only) and optional
  `FilteredFeedAdapter` interface (`LoadFromPositionFiltered`). Additive.
- **`adapters/postgres/subscription.go`**: implement `LoadFromPositionFiltered` by
  composing `positionScanQuery`'s `extraWhere` (`event_type = ANY / IN`, `stream_id =
  ANY / IN`, category `LIKE` via `escapeLikePattern`) and reusing `scanEvents`; the
  `safePositionClause` is retained so filtered reads keep the no-skip guarantee.
- **`adapters/memory/memory.go`**: implement `LoadFromPositionFiltered` as the same
  ordered position scan with an in-slice predicate, so tests and non-PG consumers get
  identical filtering semantics.
- **`store.go`**: `LoadEventsFromPositionFiltered` wrapper (type-asserts
  `FilteredFeedAdapter`, converts adapter events, returns `ErrFilteredFeedNotSupported`
  when unsupported); new sentinel error.
- **Tests**: adapter-level table tests (type-only, stream-set, category-escaping,
  empty-filter-≡-LoadFromPosition, watermark-respected, limit/order/exclusivity) for
  both adapters; a store-level test for the unsupported-adapter sentinel.
- **Docs**: doc comments framing the read as **introspection / ops / migration
  tooling, not an application read path**; `CHANGELOG` `[Unreleased]`;
  `website/docs/roadmap.md`.
- **Downstream (out of scope here; follow-up after a go-mink release)**: a consuming application re-points its shipped filtered-load call at the go-mink read for the indexed type/stream axes (dropping its copied safe-position clause and scan helper), while keeping its **app-specific, best-effort workspace text-LIKE fallback local** — the correct split (go-mink owns the indexed primitive; the consumer owns the
  policy).
- **Compatibility**: fully additive and optional. No existing signature, adapter, or
  behavior changes; the new interface is capability-detected exactly like
  `SubscriptionAdapter`.

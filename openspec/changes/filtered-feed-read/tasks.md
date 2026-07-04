## 1. Required — Adapter capability surface (`event-subscriptions`)

- [ ] 1.1 Add `FeedFilter` struct to `adapters/adapter.go` with indexed-only axes: `EventTypes []string`, `StreamIDs []string`, `Category string`; doc-comment each as index-backed and note the deliberate absence of metadata/payload axes (D2)
- [ ] 1.2 Add optional `FilteredFeedAdapter` interface (`LoadFromPositionFiltered(ctx, fromPosition uint64, limit int, filter FeedFilter) ([]StoredEvent, error)`) to `adapters/adapter.go`, mirroring `SubscriptionAdapter`
- [ ] 1.3 Add a `FeedFilter.IsEmpty()`/normalization helper (empty axes ⇒ no clause) so both adapters share the "empty ≡ unfiltered" rule

## 2. Required — PostgreSQL implementation (`event-subscriptions`)

- [ ] 2.1 Implement `(*PostgresAdapter) LoadFromPositionFiltered` by building `extraWhere` from the filter (placeholders from `$2`; `event_type IN (…)`, `stream_id IN (…)`, category `LIKE` via `escapeLikePattern`+`-%`) and calling the existing `positionScanQuery(a.schemaQ, extraWhere, limitParam)` (D3, D5)
- [ ] 2.2 Reuse `scanEvents` for row decoding; confirm the `safePositionClause` watermark is applied unchanged (it is inherited from `positionScanQuery`)
- [ ] 2.3 Empty filter path builds the exact `LoadFromPosition` query (no `extraWhere`); assert parity in a test
- [ ] 2.4 Assert `_ adapters.FilteredFeedAdapter = (*PostgresAdapter)(nil)` at compile time

## 3. Required — In-memory implementation (`event-subscriptions`)

- [ ] 3.1 Implement `(*MemoryAdapter) LoadFromPositionFiltered` as the ordered position scan (`> fromPosition`, ascending `global_position`, capped at `limit`) with an in-slice predicate: `type ∈ EventTypes`, `stream ∈ StreamIDs`, category prefix match — semantics identical to the SQL (D6)
- [ ] 3.2 Assert `_ adapters.FilteredFeedAdapter = (*MemoryAdapter)(nil)` at compile time

## 4. Required — EventStore wrapper + sentinel (`event-subscriptions`)

- [ ] 4.1 Add `ErrFilteredFeedNotSupported` sentinel (alongside `ErrSubscriptionNotSupported`)
- [ ] 4.2 Add `(*EventStore) LoadEventsFromPositionFiltered(ctx, fromPosition, limit, filter)`: type-assert `s.adapter.(adapters.FilteredFeedAdapter)`, return `ErrFilteredFeedNotSupported` when absent, else call it and convert via `convertStoredEventFromAdapter` (as `LoadEventsFromPosition` does)
- [ ] 4.3 Doc comment framing it as **introspection / ops / migration tooling, not an application read path** (D2), and cross-referencing read models for unindexed queries

## 5. Required — Tests

- [ ] 5.1 Adapter table tests (postgres + memory, shared cases where possible): type-only filter returns only that type; stream-id set; category with `_`/`%` does not over-match; multi-axis AND-composition; empty filter ≡ `LoadFromPosition`; `limit`, ascending order, and `> fromPosition` exclusivity preserved
- [ ] 5.2 Postgres: filtered read respects the gapless watermark (no event above the unstable prefix) — reuse the existing watermark test scaffold
- [ ] 5.3 Store: `LoadEventsFromPositionFiltered` on an adapter without the capability returns `ErrFilteredFeedNotSupported` (fake/base adapter)

## 6. Good-to-have — `mink` CLI inspector (`cli-tooling`, optional/separate)

- [ ] 6.1 (Optional, may land as a follow-up) `mink events --type= --stream= --category= --from= --limit=` built on `LoadEventsFromPositionFiltered`, consistent with `mink stream`/`mink projection`; prints events and supports the same filters. Tracked here as the in-repo second consumer; not required to ship the primitive

## 7. Docs, changelog & validation

- [ ] 7.1 `CHANGELOG` `[Unreleased]`: new optional filtered feed read (indexed axes; default readers unchanged)
- [ ] 7.2 Note the filtered feed read on the relevant line in `website/docs/roadmap.md`
- [ ] 7.3 `gofmt` + `go vet ./...` clean; `go test ./...` green (root + adapters); `openspec validate filtered-feed-read --strict` passes
- [ ] 7.4 Downstream note only (no code here): huisscan re-points `eventsource.LoadFilteredEvents` at the go-mink read for type/stream, keeps its workspace `data::text LIKE` fallback local — captured for the post-release follow-up

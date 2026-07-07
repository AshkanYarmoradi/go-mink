## 1. Required — Adapter capability surface (`event-subscriptions`)

- [x] 1.1 Added `FeedFilter` struct to `adapters/adapter.go` with indexed-only axes (`EventTypes`, `StreamIDs`, `Category`); each doc-commented as index-backed, with the deliberate absence of metadata/payload axes noted (D2)
- [x] 1.2 Added optional `FilteredFeedAdapter` interface (`LoadFromPositionFiltered`) to `adapters/adapter.go`, mirroring `SubscriptionAdapter`
- [x] 1.3 Added `FeedFilter.IsEmpty()` and `FeedFilter.Matches(StoredEvent)` — the shared "empty ≡ unfiltered" rule and the in-memory predicate the SQL adapter mirrors

## 2. Required — PostgreSQL implementation (`event-subscriptions`)

- [x] 2.1 `(*PostgresAdapter) LoadFromPositionFiltered` builds `extraWhere` via `buildFeedFilterWhere` (placeholders from `$2`; `event_type IN (…)`, `stream_id IN (…)`, category `LIKE` via `escapeLikePattern`+`-%`) and calls the existing `positionScanQuery` (D3, D5)
- [x] 2.2 Reuses `scanEvents`; the `safePositionClause` watermark is inherited unchanged from `positionScanQuery`
- [x] 2.3 Empty filter builds the exact `LoadFromPosition` query (no `extraWhere`); asserted by the "empty filter equals LoadFromPosition" integration test
- [x] 2.4 `_ adapters.FilteredFeedAdapter = (*PostgresAdapter)(nil)` compile-time assertion in `postgres.go`

## 3. Required — In-memory implementation (`event-subscriptions`)

- [x] 3.1 `(*MemoryAdapter) LoadFromPositionFiltered` is the ordered position scan with a `FeedFilter.Matches` predicate — semantics identical to the SQL (D6)
- [x] 3.2 `_ adapters.FilteredFeedAdapter = (*MemoryAdapter)(nil)` compile-time assertion in `memory.go`

## 4. Required — EventStore wrapper + sentinel (`event-subscriptions`)

- [x] 4.1 Added `ErrFilteredFeedNotSupported` sentinel (alongside `ErrSubscriptionNotSupported`)
- [x] 4.2 `(*EventStore) LoadEventsFromPositionFiltered` type-asserts `adapters.FilteredFeedAdapter`, returns `ErrFilteredFeedNotSupported` when absent, else calls it and converts via `convertStoredEventFromAdapter`; `FeedFilter` is aliased at the mink level (`type FeedFilter = adapters.FeedFilter`)
- [x] 4.3 Doc comment frames it as introspection / ops / migration tooling, not an application read path (D2), pointing to read models for unindexed queries

## 5. Required — Tests

- [x] 5.1 `adapters/feed_filter_test.go` (pure `Matches`/`IsEmpty`), `adapters/memory/filtered_feed_test.go`, and `adapters/postgres/filtered_feed_test.go`: type-only, stream-set, category-escaping, multi-axis AND, empty-≡-`LoadFromPosition`, `limit`/order/`fromPosition` exclusivity. `adapters/postgres` also has a **DB-free** `TestBuildFeedFilterWhere` covering SQL construction + LIKE escaping (runs in every CI, no `TEST_DATABASE_URL`)
- [x] 5.2 Watermark: the filtered read reuses `positionScanQuery`, so it inherits the gapless `safePositionClause` **by construction** — covered by the existing `event-subscriptions` watermark tests plus the "empty filter equals `LoadFromPosition`" parity test; no separate concurrent-commit test is added (it would duplicate the shared code path)
- [x] 5.3 `store_filtered_test.go`: `LoadEventsFromPositionFiltered` on a non-`FilteredFeedAdapter` (`basicEventStoreAdapter`) returns `ErrFilteredFeedNotSupported`; plus a memory-backed happy path through the store wrapper

## 6. Good-to-have — `mink` CLI inspector (`cli-tooling`, optional/separate)

- [ ] 6.1 (Deferred follow-up) `mink events --type= --stream= --category= --from= --limit=` built on `LoadEventsFromPositionFiltered`. Left for a separate change to keep this one a tight primitive; the CLI is the in-repo second consumer, not required to ship the read

## 7. Docs, changelog & validation

- [x] 7.1 `CHANGELOG` `[Unreleased]`: new optional filtered feed read (indexed axes; default readers unchanged)
- [x] 7.2 Noted the filtered feed read under Core Event Store in `website/docs/roadmap.md`; added a "Filtered feed reads" section to `website/docs/core/event-store.md`, a feature-table mention in `README.md`, and a runnable `examples/feed-filter` (registered in `examples/README.md`)
- [x] 7.3 `gofmt` clean; `go vet ./...` clean; `go test ./...` green (root + adapters, incl. PostgreSQL integration against docker-compose.test.yml); `openspec validate filtered-feed-read --strict` passes
- [x] 7.4 Downstream note only (no code here): the consumer re-points its filtered-load call at the go-mink read for the type/stream axes, keeping its workspace text-LIKE fallback local — captured for the post-release follow-up

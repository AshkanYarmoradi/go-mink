## Why

`mink` can inspect one stream (`mink stream events <id>` walks a single stream by
version) but has no way to query the **global** feed by event type or across streams.
go-mink now ships that primitive — `EventStore.LoadEventsFromPositionFiltered` over the
optional `FilteredFeedAdapter` (change `filtered-feed-read`) — and that change's own
proposal named the `mink` CLI as the in-repo second consumer, explicitly deferring the
verb to "a separate change" (its task 6.1). This is that change.

An operator auditing or debugging wants "every `OrderPlaced` since position N", "these
three streams", or "everything in the `order` category" from the shell, without writing
Go or hand-rolling SQL. A `mink events` verb closes that gap and exercises the new
adapter capability end to end, consistent with go-mink's "a perfect audit log" pitch —
which until now offered no first-class way to *query* that log by type.

## What Changes

### Required

- **`mink events` command.** A new top-level verb reading the global feed via the
  adapter's `FilteredFeedAdapter.LoadFromPositionFiltered`, exposing exactly the
  indexed-only axes the primitive carries:
  - `-t, --type` — event type(s), repeatable / comma-separated (`event_type IN …`);
  - `-s, --stream` — exact stream id(s), repeatable / comma-separated (`stream_id IN …`);
  - `-c, --category` — stream category (`stream_id LIKE '<cat>-%'`, escaped);
  - `-f, --from` — start after this global position (exclusive; default 0);
  - `-n, --limit` — max events (default 50; `0` uses the adapter cap).

  Axes AND-compose; a multi-valued axis is an OR set; an empty filter walks the whole
  feed. Output preserves the primitive's contract: ascending `global_position`,
  exclusive of `--from`, bounded by `--limit`. Default output is a scannable table.
- **`CLIAdapter` requires `FilteredFeedAdapter`.** Both built-in adapters (postgres,
  memory) already implement it, so the CLI gains the capability by compile-time
  composition — no runtime "unsupported" branch for the shipped drivers.

### Good-to-have

- **`--json` output.** A JSON array (each event with `global_position`, ids, type,
  decoded `data`, metadata, timestamp) for scripting / CSV pipelines — the huisscan
  "Operation Panel" export motivation behind the underlying primitive. Delivered here;
  the table stays the default.

### Non-Goals

- **No new filter axes.** The command inherits the primitive's indexed-only surface —
  no metadata/tenant or payload predicate. Unindexed queries stay a read-model concern;
  `mink events` is introspection, not an application read path.
- **No live tail.** This is a bounded positional read, not a filtered `SubscribeAll
  --follow`. A streaming variant can follow if a consumer needs it.
- **No format zoo.** `--json` is the single machine format; richer export (CSV,
  templating) stays in application code.
- **No new adapter/store API.** Built entirely on the existing `FilteredFeedAdapter` /
  `FeedFilter`; no signature, schema, or migration change.

## Capabilities

### Modified Capabilities

- `cli-tooling`: adds the `mink events` global-feed inspector (indexed axes — event
  type, stream id, category; positional `--from`/`--limit`; table + `--json`), built on
  the `FilteredFeedAdapter` capability. Existing CLI commands are unchanged.

## Impact

- **`cli/commands/adapter.go`**: add `adapters.FilteredFeedAdapter` to the `CLIAdapter`
  composite interface.
- **`cli/commands/events.go`** (new): `NewEventsCommand()` plus table/JSON rendering;
  builds a `FeedFilter` from flags and calls `LoadFromPositionFiltered`.
- **`cli/commands/root.go`**: register `NewEventsCommand()`.
- **Tests**: DB-free (`FeedFilter` build, table/JSON render), memory-driver command
  wiring (empty-feed branch), and a PostgreSQL integration test (seeded; per-axis;
  `--from`/`--limit`; `--json`) under the standard `-short` / `TEST_DATABASE_URL` skip.
- **Docs**: `mink events` in the CLI reference and a README/roadmap mention;
  `CHANGELOG [Unreleased]`.
- **Compatibility**: fully additive. The command is new; the `CLIAdapter` change is
  satisfied by both shipped adapters at compile time, and the lone test fake embeds
  `CLIAdapter`, so it is unaffected.

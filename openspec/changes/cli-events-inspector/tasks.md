## 1. Required — CLI capability wiring (`cli-tooling`)

- [x] 1.1 Add `adapters.FilteredFeedAdapter` to the `CLIAdapter` composite interface in `cli/commands/adapter.go` (both built-in adapters already satisfy it; the `recordFailAdapter` test fake embeds `CLIAdapter`, so it is unaffected)

## 2. Required — `mink events` command (`cli-tooling`)

- [x] 2.1 `cli/commands/events.go`: `NewEventsCommand()` with `-t/--type`, `-s/--stream` (string slices → OR/IN sets), `-c/--category`, `-f/--from` (uint64, exclusive), `-n/--limit` (default 50; `0` = adapter cap)
- [x] 2.2 Build `adapters.FeedFilter` from the flags and call `adapter.LoadFromPositionFiltered(ctx, from, limit, filter)`; an empty filter walks the whole feed; results are ascending `global_position`, exclusive of `--from`, bounded by `--limit`
- [x] 2.3 Default table render (Position / Stream / Type / Time) via `ui.NewTable`, with an empty-feed notice (exit zero, not an error); rendering factored into a pure helper over `[]adapters.StoredEvent`
- [x] 2.4 Register `NewEventsCommand()` in `root.go`; command help frames it as introspection / ops / migration tooling, not an application read path, and points unindexed queries to read models

## 3. Required — Tests

- [x] 3.1 DB-free (`events_test.go`): `FeedFilter` construction from flags (type / stream / category / empty) and the table + JSON render helpers — runs in every CI, no `TEST_DATABASE_URL`
- [x] 3.2 Memory-driver command test: `mink events` on an empty store prints the empty-feed notice and exits zero; `--json` yields `[]`; flags parse
- [x] 3.3 PostgreSQL integration (`events_pg_test.go`; skips under `-short` / no `TEST_DATABASE_URL`): seed mixed types / streams / categories, then assert `--type`, `--stream`, `--category`, `--from`, `--limit`, and `--json` filter and order correctly

## 4. Required — Docs, changelog & validation

- [x] 4.1 `CHANGELOG [Unreleased]`: new `mink events` filtered global-feed inspector
- [x] 4.2 CLI reference for `mink events` (website `cli` docs) + a README/roadmap mention of the inspector
- [x] 4.3 `gofmt` clean; `go vet ./...` clean; `go build ./...`; `go test ./cli/...` green; `openspec validate cli-events-inspector --strict` passes

## 5. Good-to-have — machine-readable output (delivered here)

- [x] 5.1 `--json` array output (each event with `global_position`, ids, `type`, decoded `data`, `metadata`, `timestamp`) for scripting / CSV pipelines (the huisscan Operation Panel export path). The table stays the default; encrypted `data` is emitted as stored (the CLI holds no keys), consistent with `mink stream export`

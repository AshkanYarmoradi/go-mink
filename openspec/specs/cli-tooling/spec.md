# cli-tooling Specification

## Purpose
TBD - created by archiving change fix-review-correctness-bugs. Update Purpose after archive.
## Requirements
### Requirement: Code generation does not overwrite existing files without --force
`mink generate` SHALL refuse to overwrite an existing target file unless a `--force`
flag is provided, so re-running generation cannot silently destroy hand-written code. In
the absence of `--force`, an existing target SHALL cause the command to fail without
writing.

#### Scenario: Re-running generate without --force preserves edits
- **WHEN** `mink generate` would write a file that already exists and `--force` is not set
- **THEN** the command returns an error and leaves the existing file unchanged

#### Scenario: --force overwrites
- **WHEN** `--force` is set and the target exists
- **THEN** the existing file is overwritten

### Requirement: migrate down treats a record-removal failure as fatal
`mink migrate down` SHALL treat a failure to remove a migration record as fatal (non-nil
error, non-zero exit), symmetric with `migrate up`'s handling of a record-write failure,
so the migrations table cannot silently diverge from the applied schema.

#### Scenario: Record removal failure aborts down with an error
- **WHEN** the down SQL succeeds but removing the migration record fails
- **THEN** `migrate down` returns an error and exits non-zero rather than reporting success

### Requirement: `mink events` queries the global feed by indexed axes
The `mink` CLI SHALL provide a top-level `events` command that reads the global event feed through the adapter's `FilteredFeedAdapter.LoadFromPositionFiltered`, filtered by the indexed axes the `FeedFilter` exposes — event type (`-t`/`--type`), exact stream id (`-s`/`--stream`), and stream category (`-c`/`--category`) — starting after a global position (`-f`/`--from`, exclusive) and bounded by a maximum count (`-n`/`--limit`). `--type` and `--stream` SHALL accept multiple values (repeatable or comma-separated) forming an OR/IN set within that axis; the axes SHALL AND-compose; an empty filter SHALL read the whole feed exactly as an unfiltered load-from-position. Results SHALL be ordered by ascending `global_position`, exclusive of `--from`, and capped at `--limit` (where `--limit` ≤ 0 falls back to the adapter's default cap). The command SHALL default to a human-readable table and MUST document itself as introspection / ops / migration tooling, not an application read path — directing unindexed criteria (tenant in metadata, payload fields) to a read-model projection.

#### Scenario: A type filter reads only matching events across streams
- **WHEN** `mink events --type OrderPlaced` runs against a feed where `OrderPlaced` spans several streams
- **THEN** only `OrderPlaced` events are printed, across all their streams, in ascending `global_position` order, up to the limit

#### Scenario: Multi-valued and multi-axis filters AND/OR-compose
- **WHEN** `mink events --type A --type B --stream s1` runs (equivalently `--type A,B`)
- **THEN** every printed event has type A or B AND stream id s1 — the set within an axis is OR, the axes are AND

#### Scenario: Category filter with metacharacters does not over-match
- **WHEN** `mink events --category "we_ird"` runs
- **THEN** only streams in that exact category (`we_ird-…`) are printed, the `_` escaped so it does not match `LIKE`-adjacent categories, consistent with `SubscribeCategory`

#### Scenario: Position window and limit are respected
- **WHEN** `mink events --from 100 --limit 10` runs
- **THEN** only events with `global_position > 100` are printed, at most 10, in ascending order

#### Scenario: An empty feed or no match is not an error
- **WHEN** `mink events` matches no events
- **THEN** the command prints a "no events" notice and exits zero rather than returning an error

### Requirement: `mink events` supports machine-readable JSON output
`mink events --json` SHALL emit the matched events as a JSON array instead of a table, each element carrying at least `global_position`, event id, `stream_id`, `version`, `type`, event `data`, `metadata`, and `timestamp`, so the feed can be piped into scripts or export pipelines. The `data` field SHALL be emitted as stored — the CLI does not hold encryption keys and MUST NOT attempt to decrypt field-encrypted payloads, consistent with `mink stream export`.

#### Scenario: JSON array output
- **WHEN** `mink events --type OrderPlaced --json` runs
- **THEN** stdout is a JSON array of the matched events, each with its `global_position` and `data`, suitable for `jq` / CSV post-processing

#### Scenario: An empty result is an empty array
- **WHEN** `mink events --json` matches nothing
- **THEN** stdout is `[]` and the command exits zero


## Context

The `filtered-feed-read` change added `FilteredFeedAdapter.LoadFromPositionFiltered`
(indexed feed filtering over the global event feed) and named the `mink` CLI as the
in-repo second consumer, deferring the verb to its own change. This change adds that
verb as a thin, faithful surface over the primitive.

## Goals / Non-Goals

- **Goals**: a thin CLI over the existing primitive; zero new adapter/store API;
  consistent with the existing `mink stream` command style; unit-testable rendering.
- **Non-Goals**: new filter axes, a live tail, output formats beyond `--json` (see the
  proposal's Non-Goals).

## Decisions

### D1: Top-level `mink events`, not another `mink stream` subcommand
`mink stream events <stream-id>` is per-stream, version-addressed, and takes a required
stream argument. The global filtered feed is position-addressed and stream-optional.
Overloading the stream subcommand would blur "one stream by version" against "the feed
by position". A sibling top-level verb keeps each read's contract distinct — mirroring
how `EventStore.LoadEventsFromPositionFiltered` is a peer of `LoadEventsFromPosition`,
not of the per-stream `Load`.

### D2: Require the capability on `CLIAdapter` (compile-time), not a runtime type-assert
Both shipped CLI adapters (postgres, memory) implement `FilteredFeedAdapter`, and the
CLI's `AdapterFactory` only ever constructs those two. Adding the method to the
`CLIAdapter` composite makes the capability a compile-time guarantee and removes a dead
"not supported" branch. (The store-level `LoadEventsFromPositionFiltered` keeps its
runtime type assertion because it accepts arbitrary third-party adapters; the CLI does
not.) The one test fake, `recordFailAdapter`, embeds `CLIAdapter`, so extending the
interface does not break it.

### D3: Flags mirror the primitive's axes 1:1 — nothing more
`--type` / `--stream` are string slices (repeatable or comma-separated) mapping to
`FeedFilter.EventTypes` / `StreamIDs` (OR/IN sets); `--category` maps to
`FeedFilter.Category`; `--from` / `--limit` map to the positional read's `fromPosition`
/ `limit`. No flag exists for an axis the `FeedFilter` does not have, so the CLI cannot
outgrow the indexed-only guarantee that keeps this an introspection tool rather than an
ad-hoc query engine.

### D4: `--limit` default 50; `0` means the adapter cap
Consistent with `mink stream list` (default 50). Both adapters coerce `limit <= 0` to
their default cap (1000) via `adapters.DefaultLimit`, so `--limit 0` is the documented
"give me a full page" escape hatch, not "zero rows".

### D5: Table by default, `--json` for machines; rendering is pure
The human default is a compact table (Position / Stream / Type / Time) for scanning an
audit feed. `--json` emits an array whose elements carry `global_position` (the field
the feed is ordered and paged by, and the one an operator pages with `--from`) plus
decoded `data` as raw JSON rather than a re-encoded string. Both renderers are pure
functions over `[]adapters.StoredEvent`, so they are unit-testable without a live
adapter (the memory driver from `getAdapter` is ephemeral and empty).

## Risks / Trade-offs

- **Coupling `CLIAdapter` to `FilteredFeedAdapter`** requires any future CLI-targeted
  adapter to implement the (small) method. Accepted: both built-ins already do, and an
  adapter that cannot read its own feed by index is not a useful `mink` target — it
  would implement the method as part of CLI support regardless.
- **Encrypted payloads**: field-encrypted events are emitted as stored, because the CLI
  does not hold encryption keys — identical to `mink stream export` today. Documented,
  not decrypted.

## Migration

None. Additive command; no config, schema, or public API change.

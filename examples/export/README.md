# GDPR Data Export Example

> Export every event belonging to a data subject — the GDPR right to access (Art. 15) and data portability (Art. 20).

When a user invokes their GDPR rights, you must produce a complete, portable copy of the data you hold on them. In an event-sourced system that data is spread across many streams, so go-mink's `DataExporter` gathers it by resolved subject footprint, by explicit stream ID, or by scanning with filters, decrypts protected fields, and returns a structured result. It also handles the awkward case where a key was already crypto-shredded: those events come back marked `Redacted` rather than crashing the export.

## What this demonstrates
- **Subject tagging** — `mink.WithSubjectTagger(subjectTagger)` records, at append time, which data subject each event concerns (`Metadata.Custom["$subjects"]`). The tagger reads `customer_id` from the event payload, never from caller-supplied metadata.
- **Subject export (recommended)** — `NewSubjectResolver` + `WithExportSubjectResolver` resolve a subject's complete footprint from those tags; `Export(ExportRequest{SubjectID})` then needs neither stream IDs nor a filter, and `ExportResult.Partial` says whether completeness could be proven.
- **Stream-based export** — pass known stream IDs in `ExportRequest.Streams` for an efficient, scan-free export.
- **Scan-based export with filters** — `SubjectFilter`, `FilterByTenantID`, `FilterByEventTypes`, `FilterByStreamPrefix` and `CombineFilters` select events when you don't know the stream IDs. `FilterByUserID` is shown for contrast: it is **actor-scoped** (who issued the command), not a subject footprint.
- **Streaming export** — `ExportStream` invokes a callback per event so large exports never materialize the whole result in memory.
- **Time-range filtering** — `FromTime` / `ToTime` scope an export to a date window.
- **Crypto-shredding + export** — after `provider.RevokeKey`, encrypted events export as `Redacted` (nil `Data`, `RedactedCount` incremented) instead of failing.

## Running
```bash
go run ./examples/export
```
No infrastructure required — encryption keys and the event store are in-memory (`local.New` + `memory.NewAdapter`). `encryption/local` is a development/testing provider; production uses `encryption/kms` or `encryption/vault`.

## What happens
The example seeds five events across two tenants (Alice under tenant A, Bob under tenant B) with `email`/`phone` encrypted and every event tagged with its `customer_id`, then runs six demos:

1. **Subject export** — a `SubjectResolver` resolves `alice-1` to her three streams and the exporter returns exactly her events, printing the footprint and `Partial=false`.
2. **Stream-based export** — Alice's three known streams are exported by ID. It prints the subject, total event count, stream list, export timestamp, and each decrypted event.
3. **Scan-based export** — `Export` scans all events with filters and prints counts for: all tenant-A events, tenant-A `OrderPlaced` events (via `CombineFilters`), all `Customer-` streams (via `FilterByStreamPrefix`), the events *tagged for* `alice-1` (via `SubjectFilter`, 3 events), and — for contrast — the events *performed by* `alice-1` (via `FilterByUserID`, 1 event: her customer record was written by `admin` and her payment by `system`).
4. **Streaming export** — `ExportStream` walks two of Alice's streams, printing one line per streamed event and the final count.
5. **Time-range export** — one export limited to the last hour (`FromTime`) and one limited to events before a future cutoff (`ToTime`), printing each matching count.
6. **Crypto-shredding + export** — Bob's data exports cleanly first; then `provider.RevokeKey("tenant-B")` is called and the re-export reports the encrypted `CustomerCreated` as `[REDACTED]` while the unencrypted `OrderPlaced` still exports as `[OK]`.

## Subject vs actor
`Metadata.UserID` records *who issued the command* (the actor). A data subject's events are frequently written by someone else — an admin, a batch job, a payment webhook — so an actor filter silently under-reports a subject's data. Tag events with a `SubjectTagger` and export through the resolver (or `SubjectFilter`) instead; the result then covers everything that concerns the subject, and `Partial` tells you when untagged legacy events make completeness unprovable.

## Key APIs
- `mink.WithSubjectTagger(func(eventType string, data []byte, md mink.Metadata) []string)` — store option that tags each appended event with its data subject(s).
- `mink.NewSubjectResolver(store, opts...)` / `mink.WithExportSubjectResolver(resolver)` — resolve a subject's complete footprint and export by `SubjectID` alone.
- `mink.SubjectFilter(subjectID)` — scan filter matching events tagged for the subject.
- `mink.NewDataExporter(store, opts...)` — construct an exporter over an event store.
- `mink.WithExportBatchSize(n)` — events loaded per batch during scan-based export (default 1000).
- `mink.ExportRequest{...}` — describes the export: `SubjectID`, `Streams`, `Filter`, `FromTime`, `ToTime`.
- `exporter.Export(ctx, req)` — returns an `*ExportResult` with `Events`, `Streams`, `TotalEvents`, `RedactedCount`, `Partial`, and `ExportedAt`.
- `exporter.ExportStream(ctx, req, handler)` — memory-efficient export invoking `handler` per `ExportedEvent`.
- `mink.FilterByTenantID(id)` / `mink.FilterByEventTypes(types...)` / `mink.FilterByStreamPrefix(prefix)` / `mink.FilterByUserID(id)` — built-in export filters (`FilterByUserID` is actor-scoped).
- `mink.CombineFilters(filters...)` — AND-compose multiple filters.
- `provider.RevokeKey(keyID)` — crypto-shred a tenant's key; subsequent exports redact its encrypted events.

## Related
- **Examples:** [encryption](../encryption) · [full-ecommerce](../full-ecommerce)
- **Docs:** [GDPR & Data Governance](https://go-mink.dev/docs/security) · [API reference](https://pkg.go.dev/go-mink.dev)

# GDPR Data Export Example

> Export every event belonging to a data subject — the GDPR right to access (Art. 15) and data portability (Art. 20).

When a user invokes their GDPR rights, you must produce a complete, portable copy of the data you hold on them. In an event-sourced system that data is spread across many streams, so go-mink's `DataExporter` gathers it by explicit stream ID or by scanning with filters, decrypts protected fields, and returns a structured result. It also handles the awkward case where a key was already crypto-shredded: those events come back marked `Redacted` rather than crashing the export.

## What this demonstrates
- **Stream-based export** — pass known stream IDs in `ExportRequest.Streams` for an efficient, scan-free export.
- **Scan-based export with filters** — `FilterByTenantID`, `FilterByEventTypes`, `FilterByStreamPrefix`, `FilterByUserID`, and `CombineFilters` select a subject's events when you don't know the stream IDs.
- **Streaming export** — `ExportStream` invokes a callback per event so large exports never materialize the whole result in memory.
- **Time-range filtering** — `FromTime` / `ToTime` scope an export to a date window.
- **Crypto-shredding + export** — after `provider.RevokeKey`, encrypted events export as `Redacted` (nil `Data`, `RedactedCount` incremented) instead of failing.

## Running
```bash
go run ./examples/export
```
No infrastructure required — encryption keys and the event store are in-memory (`local.New` + `memory.NewAdapter`).

## What happens
The example seeds five events across two tenants (Alice under tenant A, Bob under tenant B), with `email`/`phone` encrypted, then runs five demos:

1. **Stream-based export** — Alice's three known streams are exported by ID. It prints the subject, total event count, stream list, export timestamp, and each decrypted event.
2. **Scan-based export** — `Export` scans all events with filters and prints counts for: all tenant-A events, tenant-A `OrderPlaced` events (via `CombineFilters`), all `Customer-` streams (via `FilterByStreamPrefix`), and everything for user `alice-1` (via `FilterByUserID`).
3. **Streaming export** — `ExportStream` walks two of Alice's streams, printing one line per streamed event and the final count.
4. **Time-range export** — one export limited to the last hour (`FromTime`) and one limited to events before a future cutoff (`ToTime`), printing each matching count.
5. **Crypto-shredding + export** — Bob's data exports cleanly first; then `provider.RevokeKey("tenant-B")` is called and the re-export reports the encrypted `CustomerCreated` as `[REDACTED]` while the unencrypted `OrderPlaced` still exports as `[OK]`.

## Key APIs
- `mink.NewDataExporter(store, opts...)` — construct an exporter over an event store.
- `mink.WithExportBatchSize(n)` — events loaded per batch during scan-based export (default 1000).
- `mink.ExportRequest{...}` — describes the export: `SubjectID`, `Streams`, `Filter`, `FromTime`, `ToTime`.
- `exporter.Export(ctx, req)` — returns an `*ExportResult` with `Events`, `Streams`, `TotalEvents`, `RedactedCount`, and `ExportedAt`.
- `exporter.ExportStream(ctx, req, handler)` — memory-efficient export invoking `handler` per `ExportedEvent`.
- `mink.FilterByTenantID(id)` / `mink.FilterByEventTypes(types...)` / `mink.FilterByStreamPrefix(prefix)` / `mink.FilterByUserID(id)` — built-in export filters.
- `mink.CombineFilters(filters...)` — AND-compose multiple filters.
- `provider.RevokeKey(keyID)` — crypto-shred a tenant's key; subsequent exports redact its encrypted events.

## Related
- **Examples:** [encryption](../encryption) · [full-ecommerce](../full-ecommerce)
- **Docs:** [Security](https://go-mink.dev/docs/security) · [API reference](https://pkg.go.dev/go-mink.dev)

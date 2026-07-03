# subject-discovery Specification

## Purpose
TBD - created by archiving change gdpr-erasure-and-retention. Update Purpose after archive.
## Requirements
### Requirement: Subject tagging at append time
go-mink SHALL provide an optional way to tag events with the data subject(s) they concern, so a subject's footprint is discoverable later without a full-store scan. A `WithSubjectTagger(func(eventType string, data any, md Metadata) []string)` option SHALL derive zero or more subject ids per appended event and record them in `Metadata.Custom` (no DB-schema change, consistent with how crypto metadata is stored). When no tagger is configured there SHALL be zero overhead and no subject metadata is written.

#### Scenario: Tagged event is attributed to a subject
- **WHEN** a tagger is configured and an event concerning subject `u123` is appended
- **THEN** the stored event's `Metadata.Custom` records `u123` as a subject, and the event is later discoverable by that subject id

#### Scenario: Zero overhead when not configured
- **WHEN** no subject tagger is configured
- **THEN** appends write no subject metadata and incur no extra work, preserving the zero-overhead-when-unused invariant

### Requirement: SubjectResolver resolves a subject's complete footprint
The `mink` package SHALL provide a `SubjectResolver` with `Resolve(ctx, subjectID) (*SubjectFootprint, error)` that returns every stream and event pertaining to the subject, the distinct encryption key id(s) involved, and per-stream event counts. An index-backed resolver SHALL use the subject tags; a scan-based fallback SHALL use the subscription adapter (mirroring `DataExporter`'s scan path) for stores without an index. This closes the gap where `DataExporter`/`DataEraser` today require the caller to pre-enumerate `Streams` or hand-roll a subject-matching `Filter`.

#### Scenario: Resolve across multiple aggregates
- **WHEN** `Resolve` is called for a subject whose PII spans several stream namespaces (e.g. a user plus their workspaces, properties, and conversations)
- **THEN** the returned `SubjectFootprint` lists all of those streams, not only the subject's own aggregate stream

#### Scenario: Resolution without an index falls back to scan
- **WHEN** no subject index is available but a scan-capable adapter is configured
- **THEN** `Resolve` scans and matches by subject tag, returning the same footprint

#### Scenario: Resolution that cannot be complete is an error, not a silent partial
- **WHEN** neither a subject index nor a scan-capable adapter is available
- **THEN** `Resolve` returns a typed "subject resolution unavailable" error rather than silently returning a partial footprint

### Requirement: Export and erasure resolve subjects automatically
When an `ExportRequest`/`ErasureRequest` carries a `SubjectID` but no explicit `Streams` or `Filter`, `DataExporter`/`DataEraser` SHALL use the configured `SubjectResolver` to obtain the complete footprint, so callers need not pre-enumerate a subject's streams. If resolution cannot be guaranteed complete (e.g. legacy untagged events), the operation SHALL surface that via a typed error or an explicit `Partial` flag on the result rather than silently omitting data.

#### Scenario: SubjectID alone is sufficient
- **WHEN** `Erase` (or `Export`) is called with only a `SubjectID` and a resolver is configured
- **THEN** the resolver supplies the streams and the operation covers the subject's full footprint

#### Scenario: Incompleteness is never silent
- **WHEN** the resolver can only return a partial footprint
- **THEN** the result marks the operation partial and lists the unresolved areas, so the caller can remediate instead of assuming completeness

### Requirement: Footprint discovery doubles as an erasure preview
`Resolve` SHALL be a read-only operation that mutates nothing, so it can preview an erasure — what streams, events, and keys would be affected — before performing it.

#### Scenario: Preview before erase
- **WHEN** an operator calls `Resolve` for a subject prior to erasure
- **THEN** they receive the affected streams, event counts, and key ids with no data changed

### Requirement: Optional subject index with write side and backfill
The library SHALL define an OPTIONAL `SubjectIndexWriter` interface —
`IndexSubjects(ctx, streamID string, subjectIDs []string) error` — alongside the existing
read-side `SubjectIndexAdapter`, with implementations backed by a `mink_subject_index`
table on PostgreSQL (schema created only when the feature is used, preserving
no-forced-schema-change) and an in-memory map. When a writer is wired, the append path
SHALL record derived subjects to the index (best-effort, logged) so new events are
indexed, and `mink.BackfillSubjectIndex(ctx, store, tagger, writer)` SHALL populate the
index for pre-adoption (historical) events by scanning and applying the caller's tagger.

#### Scenario: Indexed resolution avoids a full scan
- **WHEN** a `SubjectIndexAdapter` is available and `Resolve` is called
- **THEN** it reads the subject's streams from the index (O(subject's events)) instead of scanning the whole store

#### Scenario: Backfill enables complete historical erasure
- **WHEN** `BackfillSubjectIndex` has populated the index for events written before tagging was enabled
- **THEN** `Resolve` returns those subjects' historical streams and no longer flags the footprint `Partial` for them


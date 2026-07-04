## ADDED Requirements

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

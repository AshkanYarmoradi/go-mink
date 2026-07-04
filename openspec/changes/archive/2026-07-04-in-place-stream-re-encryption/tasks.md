## 1. Required — In-place stream re-encryption (`in-place-stream-re-encryption`)

- [x] 1.1 Add the encode: an internal `EventStore.encryptStoredEvent(ctx, StoredEvent) (StoredEvent, error)` mirroring `prepareEventData` (run `subjectTagger` → `setSubjectTags`, then `encryptFields`); passthrough when `s.encryption == nil`, `IsEncrypted(md)`, or `!HasEncryptedFields(type)`. (Promoted to exported `EncryptStoredEvent` in task 2.)
- [x] 1.2 Add `adapters.EventRewriteAdapter` with `RewriteEventData(ctx, streamID string, version int64, data []byte, metadata Metadata) error` (optional, structural — no change to the base `EventStoreAdapter`).
- [x] 1.3 Implement `RewriteEventData` on the PostgreSQL adapter: `UPDATE <schema>.events SET data = $1, metadata = $2 WHERE stream_id = $3 AND version = $4`, encoding `metadata` with the same serializer the append path uses (so the row is byte-compatible with a natively-appended encrypted event). Error if `(streamID, version)` matches no row.
- [x] 1.4 Implement `RewriteEventData` on the in-memory adapter (replace the event in the stream slice; error if absent).
- [x] 1.5 Add `EventStore.ReEncryptStreamInPlace(ctx, streamID) (int, []string, error)` + `ErrRewriteNotSupported`: type-assert `EventRewriteAdapter`, `LoadRaw` the stream, skip already-encrypted / no-encrypted-field events (idempotent), encode + rewrite the rest, return the count and the distinct wrapping key ids (`GetEncryptionKeyID`). Return partial progress on a mid-stream error (resumable).
- [x] 1.6 Table-driven tests:
  - plaintext stream + `WithEncryptedFields` + `WithSubjectTagger` ⇒ every event's on-disk data becomes ciphertext under the subject's key (`GetEncryptionKeyID` == the tagged subject), and `id`/`type`/`version`/`global_position`/`timestamp` are unchanged.
  - After re-encryption, `Load`/`DecryptStoredEvent` return the original plaintext, and `RevokeKey(subject)` makes it decrypt-fail / export as `Redacted` (now crypto-shreddable).
  - Idempotent: a second `ReEncryptStreamInPlace` re-encrypts 0 events; a re-run after a simulated mid-stream failure completes the rest.
  - Adapter without `EventRewriteAdapter` ⇒ `ErrRewriteNotSupported`, nothing written.
  - No encryption configured ⇒ `(0, nil, nil)`, no rewrites (zero overhead).
  - Postgres round-trip: a re-encrypted row is byte-indistinguishable from a natively-appended encrypted one (decrypts via the normal read path + projections).
- [x] 1.7 Docs (`website/docs/security.md`): document `ReEncryptStreamInPlace` as the opt-in, operator-triggered historical backfill for consumers that enabled encryption after having data — idempotent, resumable, run in a maintenance window; contrast with `ReEncryptStream` (copy-to-new-stream, for rotation into a fresh stream).

## 2. Good-to-have — Exported encode primitive (`stored-event-encryption`)

- [x] 2.1 Promote 1.1 to exported `EventStore.EncryptStoredEvent(ctx, StoredEvent) (StoredEvent, error)` (the encode counterpart to `DecryptStoredEvent`); make the internal encode a thin call into it and have `ReEncryptStreamInPlace` route through it.
- [x] 2.2 Test: `EncryptStoredEvent` passthrough when unconfigured / already-encrypted; `DecryptStoredEvent(EncryptStoredEvent(e))` round-trips to the original plaintext; the produced metadata carries the current `$encryption_*` envelope.
- [x] 2.3 Docs: pair `EncryptStoredEvent` with `DecryptStoredEvent` and `ProcessStoredEvent` in the encryption/read-path reference.

## 3. CHANGELOG
- [x] 3.1 Entry under field-encryption / GDPR: in-place stream re-encryption (`ReEncryptStreamInPlace` + `EventRewriteAdapter`) and the exported `EncryptStoredEvent`, enabling historical PII to be brought under encryption for crypto-shredding; opt-in, idempotent, zero overhead when unused.

## Invariants preserved
- Append-only *history* preserved — event identity/order invariant; only at-rest encoding of data/metadata changes (the retention-RedactFields category of consumer mutation).
- Zero overhead when unused; no change to the base adapter interface; no forced schema.

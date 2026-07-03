## 1. Required — Projections receive decrypted events (`projection-engine`)

- [ ] 1.1 Add an internal `EventStore.decryptStored(ctx, StoredEvent) (StoredEvent, error)` (store.go): guard `s.encryption == nil || !IsEncrypted(stored.Metadata)` → return `stored, nil` (zero overhead); else `decryptFields(ctx, stored.StreamID, stored.Type, stored.Data, stored.Metadata)`, and on nil error swap `stored.Data`. Propagate a hard error unchanged. (Same decrypt + `DecryptionErrorHandler` path as `deserializeWithUpcast`; no upcasting — projections consume raw fields by `Type`.)
- [ ] 1.2 Async path (projection_engine.go): in `loadEventsFromPosition`, map `decryptStored` over the loaded slice before returning; on a hard decrypt error, return it (cycle error path — checkpoint not advanced, so it retries, never silently skips).
- [ ] 1.3 Inline path (projection_engine.go): in `ProcessInlineProjections`, decrypt each `StoredEvent` before the `Apply` loop, so inline and async delivery are identical; a hard error fails the inline-projection step exactly as any other inline `Apply` error.
- [ ] 1.4 Audit for any other raw-`StoredEvent` → projection delivery site (live-notify catch-up, checkpoint replay) and route it through `decryptStored` too; confirm no path reaches `Apply`/`ApplyBatch` with undecrypted `Data`.
- [ ] 1.5 Table-driven tests (encrypted store + `WithEncryptedFields` + `WithSubjectTagger`, async and inline):
  - Scalar encrypted field ⇒ projection sees plaintext, not base64 ciphertext.
  - Non-scalar encrypted field (array/object) ⇒ the projection's `json.Unmarshal` into its typed struct succeeds (regression for the corruption this fixes).
  - Crypto-shredded subject (`RevokeKey`) + nil-returning `DecryptionErrorHandler` ⇒ event delivered with **no error**, worker advances its checkpoint (does not stall/poison).
  - Hard decryption error + no handler ⇒ async cycle errors and does **not** advance the checkpoint (retried); inline append step fails.
  - No encryption configured ⇒ `StoredEvent` delivered byte-identical (zero-overhead / no-op regression guard).
  - Inline and async projections produce identical read-model state for the same encrypted stream.
- [ ] 1.6 Docs: note on the `Projection`/`ProjectionEngine` and `WithFieldEncryption` docs that projections receive **decrypted** events (same as `Load`/`LoadAggregate`/`DataExporter`); therefore read models hold plaintext and are erased via `SubjectRedactable` while the event log is crypto-shredded.

## 2. Good-to-have — Exported decrypt primitive (`stored-event-decryption`)

- [ ] 2.1 Promote 1.1 to an exported `EventStore.DecryptStoredEvent(ctx, StoredEvent) (StoredEvent, error)` (symmetric to `ProcessStoredEvent`, which returns a deserialized `Event`), and make the internal `decryptStored` a thin call into it; refactor the projection engine (1.2/1.3) to route through the exported method.
- [ ] 2.2 Route other raw-`StoredEvent` delivery surfaces through it — event subscriptions / live delivery (`event-subscriptions`) — so every raw-`StoredEvent` consumer, not just projections, gets transparent decryption from one primitive.
- [ ] 2.3 Test: `DecryptStoredEvent` passthrough when unencrypted/unconfigured (returns input, `Data` untouched); decrypts when encrypted; surfaces a hard error; and a subscription over an encrypted stream now yields plaintext.
- [ ] 2.4 Docs: document `DecryptStoredEvent` as the reusable primitive for components that consume raw `StoredEvent`s and need plaintext (pair it with `ProcessStoredEvent` in the encryption/read-path docs).

## 3. CHANGELOG
- [ ] 3.1 Entry under the field-encryption / GDPR section: projections (and, with the good-to-have, event subscriptions) now receive decrypted events — field encryption is transparent to the projection read path; backward compatible, zero overhead when unused.

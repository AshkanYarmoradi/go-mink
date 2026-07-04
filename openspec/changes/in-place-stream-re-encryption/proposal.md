# In-place stream re-encryption

## Why

Field encryption is forward-only: turning it on encrypts **new** events, but events
written before it was enabled (or under a rotated field-config) stay in whatever
at-rest encoding they had. `ReEncryptStream` exists, but it copies to a **new**
destination stream — great for key rotation into a fresh stream, unusable when the
consumer's aggregates and projections read a **fixed** stream id (`user-<id>`,
`property-<id>`). There is no way to bring a stream's *existing* events under the
current encryption scheme **in place**, so a consumer that enabled encryption after
it already had data cannot make that historical PII crypto-shreddable — the one
thing needed to erase it under Article 17.

The decrypt side of this is already solved and transparent: `DecryptStoredEvent`
(+ the projection engine) decrypt any event the store returns, keyed off each
event's own metadata. So once an old event carries the current envelope, everything
downstream already reads it correctly. What's missing is the **encode-in-place**:
run the same `encryptFields` the append path runs, then rewrite the row's `data`
and `metadata` without changing the event's identity or position.

This change adds that, as an explicit, opt-in maintenance operation — the same
category of consumer-owned mutation the append-only store already delegates for
retention `RedactFields`/`Anonymize`. It rewrites only the at-rest **encoding** of a
payload (plaintext → ciphertext of the same logical value); the event's id, type,
stream, version, global position, and timestamp are untouched, so history is
preserved, not rewritten.

## What Changes

### Required

- **`EventStore.ReEncryptStreamInPlace(ctx, streamID) (reEncrypted int, keyIDs []string, err error)`.**
  Loads the stream, and for each event that has configured encrypted fields but is
  not already encrypted under the current key, re-encrypts those fields under the
  current `FieldEncryptionConfig` (running the configured `SubjectTagger` first, so
  the wrapping key is resolved exactly as the live append path would) and rewrites
  the **same row** in place. Preserves id/type/stream/version/global-position/
  timestamp. Idempotent — an already-current event is skipped — so a re-run or a
  mid-way failure is safe to resume. Returns the count re-encrypted and the distinct
  key ids used (so the caller knows which subjects are now shreddable). No-op with
  zero overhead when no encryption is configured.
- **`adapters.EventRewriteAdapter`** — an optional, structural adapter interface
  `RewriteEventData(ctx, streamID string, version int64, data []byte, metadata Metadata) error`,
  implemented by the PostgreSQL adapter (an `UPDATE … SET data, metadata WHERE
  stream_id = $ AND version = $`) and the in-memory adapter. `ReEncryptStreamInPlace`
  type-asserts it and returns a clear "adapter does not support in-place rewrite"
  error otherwise — so existing custom adapters keep compiling and behaving exactly
  as before (opt-in, adapter-safe).

### Good-to-have

- **`EventStore.EncryptStoredEvent(ctx, stored StoredEvent) (StoredEvent, error)`** —
  the encode counterpart to the existing `DecryptStoredEvent`. Returns a
  `StoredEvent` whose configured fields are encrypted and whose metadata carries the
  current envelope (`$encryption_*` markers + wrapped DEK), by running the subject
  tagger + `encryptFields`. Passthrough when unconfigured or already encrypted. It is
  the single reusable encode primitive `ReEncryptStreamInPlace` is built on, and lets
  a consumer encode a stored event without appending.

### Non-Goals

- **Not a history rewrite.** Event identity and order are invariant — only the at-rest
  encoding of `data`/`metadata` changes (same logical value, now sealed). This is the
  same consumer-owned data-governance mutation already sanctioned for retention
  `RedactFields`/`Anonymize`; it is opt-in and operator-triggered, never automatic.
- **Not a re-key of already-current events.** Events already encrypted under the
  current key are skipped (idempotent). Rotating *from an old key to a new one* in
  place is a natural extension but is out of scope here (the first use case is
  plaintext → encrypted).
- **No read-model involvement.** Read models are the plaintext serving copy; this
  touches only the event log. Erasure still redacts read models separately.
- **No global-position / ordering change**, no snapshot invalidation beyond the
  affected stream, no forced schema (the `data`/`metadata` columns already exist).
- **No breaking change** to `EventStore`, `StoredEvent`, `FieldEncryptionConfig`, or
  the base `EventStoreAdapter`; the rewrite capability is a separate optional
  interface.

## Capabilities

### New Capabilities

- `in-place-stream-re-encryption` *(required)*: `ReEncryptStreamInPlace` +
  `EventRewriteAdapter`, bringing a fixed-id stream's existing events under the
  current encryption scheme in place, idempotently.
- `stored-event-encryption` *(good-to-have)*: `EncryptStoredEvent`, the exported
  encode primitive symmetric to `DecryptStoredEvent`.

### Modified Capabilities

<!-- None. Field encryption's append/decrypt paths are unchanged; this adds an
     encode-in-place path alongside them and a new optional adapter interface. -->

## Impact

- **Unblocks historical crypto-shredding**: a consumer that enabled encryption after
  it had data can now run a one-off backfill so old PII becomes key-revocable, closing
  the Article 17 gap for pre-encryption history without a copy-to-new-stream dance.
- **Backward compatible / zero overhead when unused**: no encryption or an adapter
  without `EventRewriteAdapter` behaves exactly as today; unencrypted stores are
  untouched.
- Pairs with `DecryptStoredEvent`/`transparent-projection-decryption`: once a row is
  re-encrypted in place, every existing read path already decrypts it transparently.

# Transparent decryption for projections

## Why

Field encryption is meant to be transparent on read: a consumer marks PII fields
with `WithEncryptedFields`, and every read path returns plaintext. That holds for
the paths that go through the serializer — `Load`, `LoadFrom`, `LoadAggregate`, and
`DataExporter` all call `deserializeWithUpcast`, which decrypts before returning
(store.go:479). The **projection engine does not**:

```go
// projection_engine.go
func (e *ProjectionEngine) loadEventsFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]StoredEvent, error) {
    return e.store.LoadEventsFromPosition(ctx, fromPosition, limit) // raw adapter bytes — no decrypt
}
```

The async worker then hands those raw `StoredEvent`s straight to
`projection.ApplyBatch` / `projection.Apply` (projection_engine.go:1056, 1064), and
the inline path (`ProcessInlineProjections`, projection_engine.go:594) delivers the
just-appended `StoredEvent`s the same way. Both projection surfaces receive
`StoredEvent`s whose encrypted `Data` fields are still ciphertext.

So a projection that reads an encrypted field gets ciphertext. For a consumer that
encrypts PII (the `subject-scoped-field-keys` use case), enabling encryption
silently corrupts read models:

- A **scalar** encrypted field (e.g. `first_name`) lands in the read model as its
  base64 ciphertext string — the read model serves garbage instead of the value.
- A **non-scalar** encrypted field (an `email_addresses` array, an `address`
  object — stored as one base64 string) fails the projection's `json.Unmarshal`
  into the typed struct, so the event never applies and the read-model row is
  missing or stale.

Read models are the serving/searchable copy (indexed columns — city, postcode,
email), so they must hold **plaintext**; erasure then redacts them separately via
`SubjectRedactable` while the event log is crypto-shredded (`RevokeKey`). That whole
design only works if the projection sees plaintext. This is the last raw read
surface — closing it makes field encryption safe to actually turn on end to end.

## What Changes

### Required

- **Projections receive decrypted events.** Both projection delivery surfaces — the
  async worker load path (`loadEventsFromPosition`) and the inline path
  (`ProcessInlineProjections`) — SHALL decrypt each field-encrypted `StoredEvent`'s
  `Data` (via the store's `FieldEncryptionConfig`, reusing the existing
  `decryptFields` path so the configured `DecryptionErrorHandler` still governs
  crypto-shredded subjects) **before** calling `ApplyBatch` / `Apply`. The event is
  still delivered as a `StoredEvent` (only its `Data` is decrypted). No-op with zero
  overhead when no encryption is configured or the event is not encrypted.

### Good-to-have

- **`EventStore.DecryptStoredEvent(ctx, StoredEvent) (StoredEvent, error)`** — an
  exported helper symmetric to the existing `ProcessStoredEvent` (which returns a
  deserialized `Event`), but returning a `StoredEvent` with decrypted `Data`. It is
  the single reusable primitive the projection engine (and any other raw-`StoredEvent`
  delivery surface — event subscriptions, live catch-up, external consumers) routes
  through, so decryption transparency is defined in exactly one place.

### Non-Goals

- **No change to the deserializing read paths.** `Load`/`LoadAggregate`/`DataExporter`
  already decrypt via `deserializeWithUpcast`; they are untouched.
- **No re-encryption or history rewrite.** Read-path only; the stored ciphertext is
  never modified (append-only preserved).
- **No new crypto-shred semantics.** A revoked key is handled exactly as elsewhere:
  `decryptFields` consults `WithDecryptionErrorHandler`; a handler that returns nil
  yields the event with its fields left as stored (still ciphertext) and **no
  error**, so the worker advances rather than stalling. A hard decryption error with
  no handler propagates as an ordinary projection-apply failure (existing
  poison/failure path), not silently swallowed.
- **No rebuild-after-erasure reconciliation.** Rebuilding a projection over a stream
  whose subject was already shredded will re-derive the (now redacted-at-source)
  read-model row from ciphertext-left events; keeping erased read models erased
  across a rebuild is a separate concern (retention/erasure-side-effects), out of
  scope here.
- **No config toggle.** Transparency is the contract; there is nothing to opt into.
  When encryption is unconfigured the guard short-circuits to the current behavior.
- **No breaking change** to `ProjectionEngine`, the `Projection` interfaces
  (`Apply`/`ApplyBatch` still take `StoredEvent`), `EventStore`, or any adapter; no
  forced schema.

## Capabilities

### New Capabilities

- `stored-event-decryption` *(good-to-have)*: `EventStore.DecryptStoredEvent(ctx,
  StoredEvent) (StoredEvent, error)` — a reusable, exported helper that returns a
  `StoredEvent` with its field-encrypted `Data` decrypted (passthrough when
  unencrypted/unconfigured), the shared primitive every raw-`StoredEvent` delivery
  surface decrypts through.

### Modified Capabilities

- `projection-engine` *(required)*: the async and inline delivery paths decrypt
  field-encrypted events before `Apply`/`ApplyBatch`, so projections see the same
  plaintext the deserializing read paths already return.

## Impact

- **Backward compatible.** Unencrypted events and stores with no encryption
  configured are byte-for-byte unaffected (`IsEncrypted` false → decrypt path
  skipped, zero overhead). Existing projections that never used encryption see no
  change.
- **Unblocks** turning field encryption on end to end: encrypting PII no longer
  corrupts read models, completing the `subject-scoped-field-keys` →
  `data-erasure` story (encrypt per subject → project plaintext → crypto-shred +
  read-model redact on erase).
- Downstream: huisscan can enable `ENCRYPTION_ENABLED` for clerk-user and property
  PII without breaking its read-model projections.

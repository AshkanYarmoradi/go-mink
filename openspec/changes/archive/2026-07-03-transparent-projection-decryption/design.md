# Design — transparent decryption for projections

## Current state (verified against the tree)

Decryption is centralized in `EventStore.deserializeWithUpcast` (store.go:479),
reached by `Load` / `LoadFrom` / `LoadAggregate` and, for raw callers, by the
exported `ProcessStoredEvent` (store.go:471) that `DataExporter` uses. It:

1. short-circuits when `s.encryption == nil || !IsEncrypted(stored.Metadata)`
   (zero overhead), else
2. calls `s.encryption.decryptFields(ctx, streamID, type, data, metadata)`
   (encryption.go:262), which honors `WithDecryptionErrorHandler` — a revoked DEK
   whose handler returns nil yields the **original** `data` (fields left as stored)
   with a nil error (encryption.go:289-291); a hard error with no handler
   propagates.

The projection engine bypasses all of this:

- **Async**: `runProjectionCycle` → `loadEventsFromPosition` (1106) →
  `store.LoadEventsFromPosition` — raw `StoredEvent`s — → `ApplyBatch` (1056) /
  `Apply` (1064).
- **Inline**: `ProcessInlineProjections` (594) → `Apply` (607) on the
  just-appended raw `StoredEvent`s.

Both `Projection.Apply(ctx, StoredEvent)` and `ApplyBatch(ctx, []StoredEvent)`
(projection.go:27,36,40) take a `StoredEvent`, so the fix must keep the delivered
value a `StoredEvent` and only swap its `Data`.

## Change

### Good-to-have primitive (do this first; Required routes through it)

A `StoredEvent`-preserving sibling of `ProcessStoredEvent`:

```go
// DecryptStoredEvent returns a copy of stored with its field-encrypted Data
// decrypted, reusing the same decrypt + DecryptionErrorHandler path as the
// deserializing read paths. Passthrough (zero-copy of Data) when no encryption is
// configured or the event is not encrypted. Upcasting is intentionally NOT applied
// here — projections consume by event Type + raw fields; upcasting stays on the
// deserializing paths.
func (s *EventStore) DecryptStoredEvent(ctx context.Context, stored StoredEvent) (StoredEvent, error) {
    if s.encryption == nil || !IsEncrypted(stored.Metadata) {
        return stored, nil
    }
    dec, err := s.encryption.decryptFields(ctx, stored.StreamID, stored.Type, stored.Data, stored.Metadata)
    if err != nil {
        return StoredEvent{}, err // hard, unhandled decryption error — caller decides
    }
    stored.Data = dec // shredded-subject case: decryptFields already returned Data unchanged, nil err
    return stored, nil
}
```

### Required — apply it on both projection surfaces

- **Async load path** — decrypt in `loadEventsFromPosition` so every downstream
  consumer (batch + sequential fallback + poison bookkeeping) sees plaintext
  uniformly:

  ```go
  func (e *ProjectionEngine) loadEventsFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]StoredEvent, error) {
      events, err := e.store.LoadEventsFromPosition(ctx, fromPosition, limit)
      if err != nil {
          return nil, err
      }
      for i := range events {
          dec, derr := e.store.DecryptStoredEvent(ctx, events[i])
          if derr != nil {
              return nil, fmt.Errorf("decrypt event at position %d: %w", events[i].GlobalPosition, derr)
          }
          events[i] = dec
      }
      return events, nil
  }
  ```

  A hard (unhandled) decryption error surfaces through the existing cycle-error
  path; it does not advance the checkpoint, so it is retried, not skipped-silently.

- **Inline path** — decrypt in `ProcessInlineProjections` before the `Apply` loop,
  so inline and async projections are byte-identical for the same stream. A hard
  error here fails the append's inline-projection step exactly as any other inline
  `Apply` error does today.

### Why not decrypt inside each projection

Pushes crypto config into every handler, is easy to forget (a new projection over
an encrypted field silently serves ciphertext), and duplicates the store's
`FieldEncryptionConfig`. Encryption is already transparent on every other read
path; projections should match, in one place.

## Failure-mode matrix

| Event | `s.encryption` | Handler | Result to projection |
|-------|----------------|---------|----------------------|
| not encrypted | any | — | delivered unchanged (zero overhead) |
| encrypted, key valid | set | — | `Data` decrypted to plaintext |
| encrypted, key revoked | set | returns nil | delivered, fields left as stored, **no error**, worker advances |
| encrypted, key revoked | set | returns err / none | error propagates → cycle retried (async) / append inline step fails |
| encrypted | **nil** | — | `IsEncrypted` true but no provider → `decryptFields` errors (misconfig, surfaced) |

## Invariants preserved

- **Append-only**: read-path only; stored ciphertext never rewritten.
- **Zero overhead when unused**: `s.encryption == nil || !IsEncrypted` guard.
- **No forced schema / no adapter API change**: operates on `StoredEvent` in memory.
- **Optional-config honored**: reuses the consumer's `DecryptionErrorHandler`.

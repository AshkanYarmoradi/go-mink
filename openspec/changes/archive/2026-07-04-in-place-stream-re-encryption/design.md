# Design — in-place stream re-encryption

## Encode primitive (mirrors the append path)

The append path stamps encryption in `prepareEventData` (store.go): run the subject
tagger to set `$subjects`, then `encryptFields` (which resolves the wrapping key from
`$subjects`). The encode primitive is the same two steps on a `StoredEvent`, and is
the exact inverse of the existing `DecryptStoredEvent`:

```go
// EncryptStoredEvent seals a stored event's configured fields under the current
// FieldEncryptionConfig, stamping the current envelope in its metadata. Passthrough
// when no encryption is configured, the event type has no encrypted fields, or the
// event is already encrypted (idempotent).
func (s *EventStore) EncryptStoredEvent(ctx context.Context, stored StoredEvent) (StoredEvent, error) {
    if s.encryption == nil || IsEncrypted(stored.Metadata) || !s.encryption.HasEncryptedFields(stored.Type) {
        return stored, nil
    }
    md := stored.Metadata
    if s.subjectTagger != nil { // resolve the key exactly as the live append would
        if subs := s.subjectTagger(stored.Type, stored.Data, md); len(subs) > 0 {
            md = setSubjectTags(md, subs)
        }
    }
    encData, encMeta, err := s.encryption.encryptFields(ctx, stored.StreamID, stored.Type, stored.Data, md)
    if err != nil {
        return StoredEvent{}, err
    }
    stored.Data, stored.Metadata = encData, encMeta
    return stored, nil
}
```

## Rewrite adapter (the only new persistence surface)

```go
// adapters — optional, structural. Implemented by postgres + memory.
type EventRewriteAdapter interface {
    // RewriteEventData replaces the data and metadata columns of the single event
    // at (streamID, version), preserving every other column (id, type,
    // global_position, timestamp). It is the in-place, data-governance write that
    // append-only stores otherwise disallow — used only via ReEncryptStreamInPlace.
    RewriteEventData(ctx context.Context, streamID string, version int64, data []byte, metadata Metadata) error
}
```

- **Postgres**: `UPDATE <schema>.events SET data = $1, metadata = $2 WHERE stream_id = $3 AND version = $4`, serializing `metadata` with the adapter's existing metadata encoder (same as append), so the rewritten row is byte-compatible with a natively-appended one.
- **Memory**: replace the matching event in the stream slice.
- Adapters implementing neither behave exactly as today.

## Orchestration

```go
func (s *EventStore) ReEncryptStreamInPlace(ctx context.Context, streamID string) (int, []string, error) {
    rw, ok := s.adapter.(adapters.EventRewriteAdapter)
    if !ok {
        return 0, nil, ErrRewriteNotSupported
    }
    events, err := s.LoadRaw(ctx, streamID, 0)   // raw, so we see current at-rest state
    if err != nil {
        return 0, nil, err
    }
    keys := map[string]struct{}{}
    n := 0
    for _, ev := range events {
        if IsEncrypted(ev.Metadata) || s.encryption == nil || !s.encryption.HasEncryptedFields(ev.Type) {
            continue // idempotent: already current, or nothing to encrypt
        }
        enc, err := s.EncryptStoredEvent(ctx, ev)
        if err != nil {
            return n, sortedKeys(keys), err // partial progress reported; resumable
        }
        if err := rw.RewriteEventData(ctx, streamID, ev.Version, enc.Data, enc.Metadata); err != nil {
            return n, sortedKeys(keys), err
        }
        if k := GetEncryptionKeyID(enc.Metadata); k != "" {
            keys[k] = struct{}{}
        }
        n++
    }
    return n, sortedKeys(keys), nil
}
```

## Failure & concurrency

| Situation | Behavior |
|-----------|----------|
| adapter has no `EventRewriteAdapter` | `ErrRewriteNotSupported`, nothing written |
| no encryption configured | loop body skipped → `(0, nil, nil)`, zero overhead |
| event already encrypted | skipped (idempotent — safe re-run / resume) |
| non-JSON / bad event data | `encryptFields` errors → returned with the count done so far; earlier rewrites stand (idempotent, so a resume picks up after them) |
| concurrent append to the same stream | unaffected — appends land at new versions; the rewrite targets existing `(streamID, version)` rows only |
| concurrent read mid-run | may observe a stream partly re-encoded; every event still decrypts on read (old plaintext or new ciphertext), so no read breaks. Operators run it in a maintenance window regardless. |

## Invariants preserved

- **History preserved, not rewritten**: id/type/stream/version/global-position/
  timestamp are untouched; only the at-rest encoding of `data`/`metadata` changes
  (same logical value). This is the retention-`RedactFields` category of consumer
  mutation, made explicit and opt-in.
- **Zero overhead when unused**: guards short-circuit with no encryption / no matching
  fields; the base adapter interface is unchanged.
- **No forced schema**: `data`/`metadata` columns already exist.
- **Transparent afterwards**: a re-encrypted row is indistinguishable from a natively
  appended encrypted one, so `DecryptStoredEvent` + projections already read it.

## Rejected

- **Extend `ReEncryptStream` to accept `dst == src`.** It appends to the destination
  with strict expected-version from `NoStream`, so writing back to the source stream
  would always version-conflict. In-place needs a row rewrite, not an append.
- **Reimplement the envelope in the consumer.** Couples every consumer to the
  `$encryption_*` format; the encode must stay behind `EncryptStoredEvent` where
  `encryptFields` lives.

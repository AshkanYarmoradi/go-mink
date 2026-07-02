package mink

import (
	"context"
	"fmt"
)

// Key lifecycle helpers.
//
// Master-key rotation is inherently transparent in go-mink: every event stamps the
// key id it was encrypted under in its metadata, so rotating the default key
// (WithDefaultKeyID) only affects NEW appends — existing events keep decrypting
// under their original key as long as the provider/KMS still holds it. With AWS KMS
// and Vault Transit, native master-key rotation is likewise transparent (the key id
// is stable). No event rows are rewritten.

// ReEncryptStream re-encrypts a stream's events into dstStreamID under the event
// store's CURRENT encryption configuration (e.g. a freshly rotated default key),
// WITHOUT mutating the source stream. This is the append-only way to re-encrypt
// after a suspected key compromise: events are read (decrypted under the old key),
// then re-appended to a new stream (re-encrypted under the new key).
//
// IMPORTANT — this function does NOT erase anything. The SOURCE stream and its
// old-key-recoverable PII remain fully intact and decryptable afterward; a compromised
// old key still reads the source. Re-encryption is complete only when the caller ALSO
// retires the source stream and revokes the old key id(s) — which is why the old key
// ids are returned. Until then the PII exists under both keys.
//
// The source stream must still be decryptable (its key not yet revoked). Non-PII
// metadata (correlation/causation/tenant/subject tags) is carried over; stale
// $encryption_* markers are stripped so the new append re-stamps them for the current
// key. The destination is appended with strict expected-version checks starting from
// NoStream, so a re-run against an existing destination fails instead of silently
// duplicating the copy. Returns the number of events copied and the distinct old key
// ids (for the caller to revoke).
func ReEncryptStream(ctx context.Context, store *EventStore, srcStreamID, dstStreamID string) (copied int, oldKeyIDs []string, err error) {
	if srcStreamID == "" || dstStreamID == "" {
		return 0, nil, ErrEmptyStreamID
	}
	events, err := store.Load(ctx, srcStreamID)
	if err != nil {
		return 0, nil, err
	}
	oldSet := make(map[string]struct{})
	for _, ev := range events {
		if k := GetEncryptionKeyID(ev.Metadata); k != "" {
			oldSet[k] = struct{}{}
		}
	}
	oldKeyIDs = sortedSet(oldSet)

	for i, ev := range events {
		// Re-encryption re-appends by value, so the store must be able to derive the event
		// type from ev.Data (GetEventType). An unregistered stored type deserializes to a
		// map fallback whose type name is empty; appending it would fail deep in the
		// serializer with an opaque error. Fail here with an actionable one instead.
		if GetEventType(ev.Data) == "" {
			return i, oldKeyIDs, fmt.Errorf(
				"mink: ReEncryptStream: cannot derive a Go event type for event %d of stream %q (stored type %q, version %d) — register the event type (RegisterEvents) before re-encrypting",
				i, srcStreamID, ev.Type, ev.Version)
		}
		md := stripEncryptionMarkers(ev.Metadata)
		// Expected version i: 0 (NoStream) for the first event, then the running
		// version — so a re-run against an existing destination errors rather than
		// appending a duplicate copy.
		if err := store.Append(ctx, dstStreamID, []interface{}{ev.Data},
			WithAppendMetadata(md), ExpectVersion(int64(i))); err != nil {
			return i, oldKeyIDs, err
		}
	}
	return len(events), oldKeyIDs, nil
}

// stripEncryptionMarkers returns a copy of m with the $encryption_* markers removed,
// leaving all other custom metadata (subject tags, correlation ids) intact. Used when
// re-appending a decrypted event so the append path re-stamps markers for the current
// key instead of carrying stale ones (e.g. after a field-config change).
func stripEncryptionMarkers(m Metadata) Metadata {
	if len(m.Custom) == 0 {
		return m
	}
	nc := make(map[string]string, len(m.Custom))
	for k, v := range m.Custom {
		switch k {
		case encryptedFieldsKey, encryptionKeyIDKey, encryptedDEKKey, encryptionAlgorithmKey:
			continue
		default:
			nc[k] = v
		}
	}
	m.Custom = nc
	return m
}

package mink

import "context"

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
// then re-appended to a new stream (re-encrypted under the new key). The original
// stream is left intact and can be retired once the copy is verified.
//
// The source stream must still be decryptable (its key not yet revoked). Non-PII
// metadata (correlation/causation/tenant/subject tags) is carried over; the
// encryption markers are re-stamped for the new key. Returns the number of events
// re-encrypted.
func ReEncryptStream(ctx context.Context, store *EventStore, srcStreamID, dstStreamID string) (int, error) {
	if srcStreamID == "" || dstStreamID == "" {
		return 0, ErrEmptyStreamID
	}
	events, err := store.Load(ctx, srcStreamID)
	if err != nil {
		return 0, err
	}
	for _, ev := range events {
		if err := store.Append(ctx, dstStreamID, []interface{}{ev.Data},
			WithAppendMetadata(ev.Metadata)); err != nil {
			return 0, err
		}
	}
	return len(events), nil
}

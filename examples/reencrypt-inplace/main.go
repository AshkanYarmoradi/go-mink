// Package main demonstrates in-place stream re-encryption — the historical backfill
// for a store that enabled field encryption AFTER it already had plaintext data.
//
// It shows:
//   - appending events as plaintext (encryption not yet configured)
//   - turning encryption on and calling store.ReEncryptStreamInPlace to seal the
//     existing rows in place (same stream id / version / global position)
//   - the events decrypting transparently afterwards
//   - crypto-shredding the now-encrypted history by revoking the key
//
// Unlike ReEncryptStream (which copies to a NEW stream, for key rotation), this
// rewrites the SAME rows, so it fits aggregates that read a fixed stream id.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"strings"

	mink "go-mink.dev"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// UserRegistered carries PII (Email) we want encrypted at rest.
type UserRegistered struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

func main() {
	ctx := context.Background()
	adapter := memory.NewAdapter() // one adapter, shared by both stores below
	const stream = "user-u1"

	// 1) History written BEFORE encryption was enabled — plaintext on disk.
	plain := mink.New(adapter)
	plain.RegisterEvents(UserRegistered{})
	if err := plain.Append(ctx, stream, []interface{}{
		UserRegistered{UserID: "u1", Email: "ada@example.com"},
	}); err != nil {
		log.Fatal(err)
	}
	raw, _ := plain.LoadRaw(ctx, stream, 0)
	fmt.Printf("before backfill: on-disk=%s encrypted=%v\n", raw[0].Data, mink.IsEncrypted(raw[0].Metadata))

	// 2) Turn encryption on: same adapter, now with a field-encryption config.
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	provider, err := local.New(local.WithKey("k", key))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = provider.Close() }()

	enc := mink.New(adapter, mink.WithFieldEncryption(mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("k"),
		mink.WithEncryptedFields("UserRegistered", "email"),
	)))
	enc.RegisterEvents(UserRegistered{})

	// 3) Backfill: seal the existing rows in place. Idempotent + resumable.
	n, keyIDs, err := enc.ReEncryptStreamInPlace(ctx, stream)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("backfilled %d event(s) under key(s) %v\n", n, keyIDs)

	raw, _ = enc.LoadRaw(ctx, stream, 0)
	fmt.Printf("after backfill:  on-disk-has-plaintext=%v encrypted=%v (version %d preserved)\n",
		strings.Contains(string(raw[0].Data), "ada@example.com"), mink.IsEncrypted(raw[0].Metadata), raw[0].Version)

	// 4) It still decrypts transparently on read...
	events, _ := enc.Load(ctx, stream)
	fmt.Printf("decrypted on read: %+v\n", events[0].Data)

	// 5) ...and is now crypto-shreddable: revoke the key → unrecoverable.
	if err := provider.RevokeKey("k"); err != nil {
		log.Fatal(err)
	}
	if _, err := enc.Load(ctx, stream); err != nil {
		fmt.Printf("after crypto-shred: load fails as expected (%v)\n", err)
	}
}

package local

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
)

func TestProvider_GetKey_ReturnsCopyNotLiveSlice(t *testing.T) {
	key := testKey(t)
	p := mustNew(t, WithKey("master-1", key))
	defer func() { _ = p.Close() }()

	got, err := p.getKey("master-1")
	require.NoError(t, err)
	require.Equal(t, key, got)
	assert.NotSame(t, &p.keys["master-1"][0], &got[0], "getKey must not alias the stored slice")

	// Callers clear their copy when done; the stored key must survive that.
	encryption.ClearBytes(got)
	again, err := p.getKey("master-1")
	require.NoError(t, err)
	assert.Equal(t, key, again)

	// And the provider still works end to end after a caller cleared its copy.
	ct, err := p.Encrypt(context.Background(), "master-1", []byte("secret"))
	require.NoError(t, err)
	pt, err := p.Decrypt(context.Background(), "master-1", ct)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(pt))
}

// TestProvider_CryptoOps_ConcurrentTeardown_NoKeyCorruption runs encrypt and
// data-key operations concurrently with RevokeKey / Close. Before getKey returned
// a copy, the live master-key slice was handed out and then zeroed in place by
// RevokeKey/Close — a data race (flagged by `go test -race`) that could key an
// in-flight cipher with partially-zeroed bytes. Every operation that succeeds
// must therefore have used the intact original key, and every failure must be
// one of the two legitimate outcomes (revoked / closed).
func TestProvider_CryptoOps_ConcurrentTeardown_NoKeyCorruption(t *testing.T) {
	teardowns := []struct {
		name string
		fn   func(p *Provider) error
	}{
		{"RevokeKey", func(p *Provider) error { return p.RevokeKey("k") }},
		{"Close", func(p *Provider) error { return p.Close() }},
	}
	for _, td := range teardowns {
		t.Run(td.name, func(t *testing.T) {
			for round := 0; round < 25; round++ {
				runConcurrentTeardownRound(t, td.fn)
			}
		})
	}
}

func runConcurrentTeardownRound(t *testing.T, teardown func(p *Provider) error) {
	t.Helper()
	key := testKey(t)
	p := mustNew(t, WithKey("k", key))
	defer func() { _ = p.Close() }()

	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			runCryptoOpsUntilTeardown(t, p, key)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		assert.NoError(t, teardown(p))
	}()
	close(start)
	wg.Wait()
}

// runCryptoOpsUntilTeardown drives Encrypt and GenerateDataKey against p until the
// first error. Every success must have been sealed under the intact master key and
// the first error must be one of the two legitimate teardown outcomes.
func runCryptoOpsUntilTeardown(t *testing.T, p *Provider, key []byte) {
	ctx := context.Background()
	aad := []byte("k") // the local provider binds ciphertext to the key id
	for i := 0; i < 50; i++ {
		ct, err := p.Encrypt(ctx, "k", []byte("secret"))
		if err != nil {
			assertLegitimateTeardownFailure(t, "Encrypt", err)
			return
		}
		assertSealedUnderKey(t, key, ct, aad, []byte("secret"), "ciphertext must have been produced under the intact key")

		dk, err := p.GenerateDataKey(ctx, "k")
		if err != nil {
			assertLegitimateTeardownFailure(t, "GenerateDataKey", err)
			return
		}
		assertSealedUnderKey(t, key, dk.Ciphertext, aad, dk.Plaintext, "wrapped DEK must have been sealed under the intact key")
		encryption.ClearBytes(dk.Plaintext)
	}
}

// assertLegitimateTeardownFailure accepts only the errors a concurrent RevokeKey or
// Close may legitimately cause.
func assertLegitimateTeardownFailure(t *testing.T, op string, err error) {
	t.Helper()
	legitimate := errors.Is(err, encryption.ErrKeyRevoked) || errors.Is(err, encryption.ErrProviderClosed)
	assert.True(t, legitimate, "unexpected %s error: %v", op, err)
}

// assertSealedUnderKey proves ciphertext was produced under key (not a partially
// zeroed copy of it) by decrypting it independently of the provider.
func assertSealedUnderKey(t *testing.T, key, ciphertext, aad, wantPlaintext []byte, msg string) {
	t.Helper()
	pt, err := encryption.AESGCMDecrypt(key, ciphertext, aad)
	if assert.NoError(t, err, msg) {
		assert.Equal(t, wantPlaintext, pt)
	}
}

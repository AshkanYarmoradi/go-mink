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
	ctx := context.Background()
	aad := []byte("k") // the local provider binds ciphertext to the key id

	legitimateFailure := func(err error) bool {
		return errors.Is(err, encryption.ErrKeyRevoked) || errors.Is(err, encryption.ErrProviderClosed)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 50; i++ {
				ct, err := p.Encrypt(ctx, "k", []byte("secret"))
				if err != nil {
					assert.True(t, legitimateFailure(err), "unexpected Encrypt error: %v", err)
					return
				}
				pt, derr := encryption.AESGCMDecrypt(key, ct, aad)
				if assert.NoError(t, derr, "ciphertext must have been produced under the intact key") {
					assert.Equal(t, "secret", string(pt))
				}

				dk, err := p.GenerateDataKey(ctx, "k")
				if err != nil {
					assert.True(t, legitimateFailure(err), "unexpected GenerateDataKey error: %v", err)
					return
				}
				unwrapped, derr := encryption.AESGCMDecrypt(key, dk.Ciphertext, aad)
				if assert.NoError(t, derr, "wrapped DEK must have been sealed under the intact key") {
					assert.Equal(t, dk.Plaintext, unwrapped)
				}
				encryption.ClearBytes(dk.Plaintext)
			}
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

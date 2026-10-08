package vault

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
)

// recordingVaultClient records every key name it is handed and implements the
// optional revocation API, so tests can prove unsafe names never reach Vault and
// safe names arrive unchanged (or correctly escaped).
type recordingVaultClient struct {
	names []string
	// existsFunc overrides KeyExists when set (blocking, error injection, ctx capture).
	existsFunc func(ctx context.Context, keyName string) (bool, error)
	// existsCtx is the context of the most recent KeyExists call.
	existsCtx context.Context //nolint:containedctx // test double records the ctx it was called with
}

func (c *recordingVaultClient) Encrypt(_ context.Context, keyName string, plaintext []byte) ([]byte, error) {
	c.names = append(c.names, keyName)
	return append([]byte("vault:"), plaintext...), nil
}

func (c *recordingVaultClient) Decrypt(_ context.Context, keyName string, ciphertext []byte) ([]byte, error) {
	c.names = append(c.names, keyName)
	if len(ciphertext) > 6 && string(ciphertext[:6]) == "vault:" {
		return ciphertext[6:], nil
	}
	return nil, errors.New("vault: decryption failed")
}

func (c *recordingVaultClient) DeleteKey(_ context.Context, keyName string) error {
	c.names = append(c.names, keyName)
	return nil
}

func (c *recordingVaultClient) KeyExists(ctx context.Context, keyName string) (bool, error) {
	c.names = append(c.names, keyName)
	c.existsCtx = ctx
	if c.existsFunc != nil {
		return c.existsFunc(ctx, keyName)
	}
	return true, nil
}

func TestTransitKeyName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{"simple", "customer-42", "customer-42", ""},
		{"unreserved alphabet passes unchanged", "Tenant_A.v2-key~x", "Tenant_A.v2-key~x", ""},
		{"empty", "", "", "must not be empty"},
		{"path traversal", "../sys/policies/acl/root", "", "'/'"},
		{"nested segment", "a/b", "", "'/'"},
		{"bare dot-dot segment", "..", "", "relative path segment"},
		{"bare dot segment", ".", "", "relative path segment"},
		{"control character newline", "a\nb", "", "control byte"},
		{"control character NUL", "a\x00b", "", "control byte"},
		{"control character DEL", "a\x7fb", "", "control byte"},
		// Relaxed: these are legal Vault key names a deployment may already use;
		// they are neutralized by escaping, never rejected.
		{"dot-dot inside a name", "a..b", "a..b", ""},
		{"leading dots", "..a", "..a", ""},
		{"space is escaped", "a b", "a%20b", ""},
		{"query string is escaped", "k?list=true", "k%3Flist=true", ""},
		{"fragment is escaped", "k#x", "k%23x", ""},
		{"percent-encoded slash cannot decode to a slash", "a%2Fb", "a%252Fb", ""},
		{"backslash is escaped", `a\b`, "a%5Cb", ""},
		{"non-ascii is escaped", "clé", "cl%C3%A9", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := transitKeyName(tt.in)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func assertKeyNameRejected(t *testing.T, err error, keyID string, sentinel error) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	var ee *encryption.EncryptionError
	require.True(t, errors.As(err, &ee), "expected *encryption.EncryptionError, got %T", err)
	assert.Equal(t, keyID, ee.KeyID)
	assert.NotErrorIs(t, err, encryption.ErrKeyRevoked, "a rejected name must not be mistaken for a revoked key")
}

func TestProvider_RejectsUnsafeKeyNames(t *testing.T) {
	for _, bad := range []string{"", "../sys/policies/acl/root", "a/b", "..", ".", "a\nb", "a\x00b"} {
		t.Run(fmt.Sprintf("%q", bad), func(t *testing.T) {
			client := &recordingVaultClient{}
			p := New(WithVaultClient(client))
			defer func() { _ = p.Close() }()
			ctx := context.Background()

			_, err := p.Encrypt(ctx, bad, []byte("x"))
			assertKeyNameRejected(t, err, bad, encryption.ErrEncryptionFailed)
			_, err = p.GenerateDataKey(ctx, bad)
			assertKeyNameRejected(t, err, bad, encryption.ErrEncryptionFailed)
			_, err = p.Decrypt(ctx, bad, []byte("vault:x"))
			assertKeyNameRejected(t, err, bad, encryption.ErrDecryptionFailed)
			_, err = p.DecryptDataKey(ctx, bad, []byte("vault:x"))
			assertKeyNameRejected(t, err, bad, encryption.ErrDecryptionFailed)
			err = p.RevokeKey(bad)
			assertKeyNameRejected(t, err, bad, encryption.ErrEncryptionFailed)
			_, err = p.IsRevoked(bad)
			assertKeyNameRejected(t, err, bad, encryption.ErrDecryptionFailed)

			assert.Empty(t, client.names, "an unsafe key name must never reach the Vault client")
		})
	}
}

func TestProvider_ValidKeyNamePassedThroughUnchanged(t *testing.T) {
	client := &recordingVaultClient{}
	p := New(WithVaultClient(client))
	defer func() { _ = p.Close() }()
	ctx := context.Background()
	const name = "Tenant_A.v2-key"

	ct, err := p.Encrypt(ctx, name, []byte("x"))
	require.NoError(t, err)
	pt, err := p.Decrypt(ctx, name, ct)
	require.NoError(t, err)
	assert.Equal(t, "x", string(pt))
	dk, err := p.GenerateDataKey(ctx, name)
	require.NoError(t, err)
	assert.Equal(t, name, dk.KeyID)
	unwrapped, err := p.DecryptDataKey(ctx, name, dk.Ciphertext)
	require.NoError(t, err)
	assert.Equal(t, dk.Plaintext, unwrapped)
	_, err = p.IsRevoked(name)
	require.NoError(t, err)
	require.NoError(t, p.RevokeKey(name))

	// Encrypt, Decrypt, GenerateDataKey→Encrypt, DecryptDataKey→Decrypt,
	// IsRevoked→KeyExists, RevokeKey→KeyExists+DeleteKey.
	require.Len(t, client.names, 7)
	for _, got := range client.names {
		assert.Equal(t, name, got)
	}
}

// A pre-existing deployment that named keys with characters outside the old
// allowlist (spaces, non-ASCII, '?') is not locked out: the name is accepted and
// reaches the client path-escaped, so a URL-concatenating client stays safe and
// Vault decodes it back to the original key name. DataKey.KeyID keeps the
// caller's original id — that is what the event store stamps into metadata.
func TestProvider_LegacyKeyNamesReachClientEscaped(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"tenant 42", "tenant%2042"},
		{"clé", "cl%C3%A9"},
		{"k?list=true", "k%3Flist=true"},
		{"a..b", "a..b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &recordingVaultClient{}
			p := New(WithVaultClient(client))
			defer func() { _ = p.Close() }()
			ctx := context.Background()

			dk, err := p.GenerateDataKey(ctx, tt.name)
			require.NoError(t, err)
			assert.Equal(t, tt.name, dk.KeyID, "the stamped key id is the caller's original id")
			unwrapped, err := p.DecryptDataKey(ctx, tt.name, dk.Ciphertext)
			require.NoError(t, err)
			assert.Equal(t, dk.Plaintext, unwrapped)
			require.NoError(t, p.RevokeKey(tt.name))

			require.NotEmpty(t, client.names)
			for _, got := range client.names {
				assert.Equal(t, tt.want, got, "the client receives the path-escaped segment")
			}
		})
	}
}

// --- Revocation timeout (mirrors kms.WithRevocationTimeout) ---

func TestWithRevocationTimeout(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		assert.Equal(t, DefaultRevocationTimeout, New(WithVaultClient(&recordingVaultClient{})).revocationTimeout)
	})
	t.Run("non-positive values are ignored", func(t *testing.T) {
		assert.Equal(t, DefaultRevocationTimeout, New(WithVaultClient(&recordingVaultClient{}), WithRevocationTimeout(0)).revocationTimeout)
		assert.Equal(t, DefaultRevocationTimeout, New(WithVaultClient(&recordingVaultClient{}), WithRevocationTimeout(-time.Second)).revocationTimeout)
	})
	t.Run("zero-value provider still bounds calls", func(t *testing.T) {
		p := &Provider{}
		assert.Equal(t, DefaultRevocationTimeout, p.timeout())
	})
	t.Run("context-free methods carry the configured deadline", func(t *testing.T) {
		client := &recordingVaultClient{}
		p := New(WithVaultClient(client), WithRevocationTimeout(90*time.Second))
		defer func() { _ = p.Close() }()

		calls := map[string]func() error{
			"RevokeKey": func() error { return p.RevokeKey("k") },
			"IsRevoked": func() error { _, err := p.IsRevoked("k"); return err },
		}
		for name, call := range calls {
			client.existsCtx = nil
			require.NoError(t, call(), name)
			require.NotNil(t, client.existsCtx, name)
			dl, ok := client.existsCtx.Deadline()
			require.True(t, ok, "%s must bound its Vault calls", name)
			assert.WithinDuration(t, time.Now().Add(90*time.Second), dl, 5*time.Second, name)
		}
	})
	t.Run("a stalled Vault times out instead of hanging", func(t *testing.T) {
		client := &recordingVaultClient{}
		client.existsFunc = func(ctx context.Context, _ string) (bool, error) {
			<-ctx.Done()
			return false, ctx.Err()
		}
		p := New(WithVaultClient(client), WithRevocationTimeout(20*time.Millisecond))
		defer func() { _ = p.Close() }()

		start := time.Now()
		err := p.RevokeKey("k")
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
		assert.Less(t, time.Since(start), 5*time.Second)

		start = time.Now()
		_, err = p.IsRevoked("k")
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, time.Since(start), 5*time.Second)
	})
}

type ctxKey struct{}

func TestProvider_DecryptErrorPath_ThreadsCallerContext(t *testing.T) {
	t.Run("values and deadline propagate to the probe", func(t *testing.T) {
		client := &recordingVaultClient{}
		client.existsFunc = func(context.Context, string) (bool, error) { return false, nil } // deleted
		p := New(WithVaultClient(client))
		defer func() { _ = p.Close() }()

		deadline := time.Now().Add(time.Hour)
		ctx, cancel := context.WithDeadline(context.WithValue(context.Background(), ctxKey{}, "trace-1"), deadline)
		defer cancel()

		_, err := p.Decrypt(ctx, "k", []byte("bad"))
		require.ErrorIs(t, err, encryption.ErrKeyRevoked)
		require.NotNil(t, client.existsCtx)
		assert.Equal(t, "trace-1", client.existsCtx.Value(ctxKey{}))
		got, ok := client.existsCtx.Deadline()
		require.True(t, ok)
		assert.True(t, got.Equal(deadline), "the caller's own deadline is kept, not replaced")
	})
	t.Run("no caller deadline: the revocation timeout bounds the probe", func(t *testing.T) {
		client := &recordingVaultClient{}
		client.existsFunc = func(context.Context, string) (bool, error) { return false, nil }
		p := New(WithVaultClient(client))
		defer func() { _ = p.Close() }()

		_, err := p.DecryptDataKey(context.Background(), "k", []byte("bad"))
		require.ErrorIs(t, err, encryption.ErrKeyRevoked)
		require.NotNil(t, client.existsCtx)
		dl, ok := client.existsCtx.Deadline()
		require.True(t, ok)
		assert.WithinDuration(t, time.Now().Add(DefaultRevocationTimeout), dl, 5*time.Second)
	})
	t.Run("an unverifiable key state fails closed as a decryption error", func(t *testing.T) {
		client := &recordingVaultClient{}
		client.existsFunc = func(ctx context.Context, _ string) (bool, error) { return false, ctx.Err() }
		p := New(WithVaultClient(client))
		defer func() { _ = p.Close() }()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := p.Decrypt(ctx, "k", []byte("bad"))
		require.Error(t, err)
		assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
		assert.NotErrorIs(t, err, encryption.ErrKeyRevoked, "an unverifiable key state must not be reported as revoked")
	})
}

package mink_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/encryption"
	"go-mink.dev/encryption/vault"
)

// vaultTransitClient is a stdlib-only HashiCorp Vault Transit client implementing both
// encryption/vault.VaultClient (Encrypt/Decrypt) and VaultRevocationClient (DeleteKey/KeyExists),
// so the provider is fully Revocable. It talks to a Vault dev container over HTTP.
type vaultTransitClient struct {
	addr  string
	token string
	http  *http.Client
}

func (c *vaultTransitClient) do(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.addr+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", c.token)
	return c.http.Do(req)
}

func (c *vaultTransitClient) Encrypt(ctx context.Context, keyName string, plaintext []byte) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/transit/encrypt/"+keyName,
		map[string]string{"plaintext": base64.StdEncoding.EncodeToString(plaintext)})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault encrypt: status %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return []byte(out.Data.Ciphertext), nil
}

func (c *vaultTransitClient) Decrypt(ctx context.Context, keyName string, ciphertext []byte) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/transit/decrypt/"+keyName,
		map[string]string{"ciphertext": string(ciphertext)})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault decrypt: status %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.Data.Plaintext)
}

func (c *vaultTransitClient) DeleteKey(ctx context.Context, keyName string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/v1/transit/keys/"+keyName, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vault delete key: status %d", resp.StatusCode)
	}
	return nil
}

func (c *vaultTransitClient) KeyExists(ctx context.Context, keyName string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/transit/keys/"+keyName, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("vault key exists: status %d", resp.StatusCode)
	}
}

func (c *vaultTransitClient) ensureKey(ctx context.Context, t *testing.T, keyName string) {
	t.Helper()
	// Enable the transit engine (ignore "already mounted").
	_, _ = c.do(ctx, http.MethodPost, "/v1/sys/mounts/transit", map[string]string{"type": "transit"})
	// Create the key and allow deletion (so crypto-shred can delete it).
	resp, err := c.do(ctx, http.MethodPost, "/v1/transit/keys/"+keyName, nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	resp, err = c.do(ctx, http.MethodPost, "/v1/transit/keys/"+keyName+"/config", map[string]bool{"deletion_allowed": true})
	require.NoError(t, err)
	_ = resp.Body.Close()
}

// TestE2E_Vault_EncryptRevoke: the Vault Transit provider round-trips a data key against a real
// Vault dev container, and revoking the key (crypto-shred) makes a subsequent decrypt fail.
// Gated on VAULT_ADDR (self-skips otherwise), e.g. a `hashicorp/vault` dev container.
func TestE2E_Vault_EncryptRevoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Vault E2E in short mode")
	}
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		t.Skip("VAULT_ADDR not set; skipping Vault E2E")
	}
	token := os.Getenv("VAULT_TOKEN")
	if token == "" {
		token = "root"
	}
	ctx := context.Background()
	keyName := fmt.Sprintf("e2e-vault-%d", time.Now().UnixNano())

	client := &vaultTransitClient{addr: addr, token: token, http: &http.Client{Timeout: 5 * time.Second}}
	client.ensureKey(ctx, t, keyName)
	provider := vault.New(vault.WithVaultClient(client))
	t.Cleanup(func() { _ = provider.Close() })

	// Round-trip a data key.
	dk, err := provider.GenerateDataKey(ctx, keyName)
	require.NoError(t, err)
	pt, err := provider.DecryptDataKey(ctx, keyName, dk.Ciphertext)
	require.NoError(t, err)
	assert.Equal(t, dk.Plaintext, pt, "data key round-trips through real Vault Transit")

	// Crypto-shred: revoke the key, then decrypt must fail with ErrKeyRevoked.
	require.NoError(t, provider.RevokeKey(keyName))
	revoked, err := provider.IsRevoked(keyName)
	require.NoError(t, err)
	assert.True(t, revoked, "the key reports revoked after deletion")

	_, err = provider.DecryptDataKey(ctx, keyName, dk.Ciphertext)
	require.Error(t, err, "decrypt after revoke fails")
	assert.True(t, errors.Is(err, encryption.ErrKeyRevoked), "the failure is a KeyRevokedError")
}

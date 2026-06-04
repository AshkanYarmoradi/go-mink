package vault

// Credential-gated integration test for the HashiCorp Vault Transit provider.
//
// This test talks to a real Vault server's Transit engine and is skipped unless
// the required environment is present (mirrors the Postgres skip pattern):
//
//	VAULT_ADDR  – base URL of the Vault server (e.g. http://127.0.0.1:8200)
//	VAULT_TOKEN – a token with access to the Transit engine
//	MINK_VAULT_TEST_KEY – name of the Transit key to exercise
//	                      (defaults to "mink-test"; the key must already exist)
//
// It is also skipped under `go test -short`. When configured, it performs a
// GenerateDataKey -> DecryptDataKey round-trip and asserts the unwrapped DEK
// matches the plaintext returned by GenerateDataKey.
//
// The Vault client below is a minimal stdlib-only implementation of the
// VaultClient interface (net/http + encoding/json), so this adds no new module
// dependency. Production callers are expected to inject their own client (e.g.
// one wrapping the official Vault SDK).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
)

// httpTransitClient is a minimal Vault Transit client for integration testing.
type httpTransitClient struct {
	addr  string
	token string
	hc    *http.Client
}

func (c *httpTransitClient) do(ctx context.Context, path string, reqBody, respOut any) error {
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.addr+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vault returned status %d: %s", resp.StatusCode, string(body))
	}

	return json.Unmarshal(body, respOut)
}

func (c *httpTransitClient) Encrypt(ctx context.Context, keyName string, plaintext []byte) ([]byte, error) {
	reqBody := map[string]string{"plaintext": base64.StdEncoding.EncodeToString(plaintext)}
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := c.do(ctx, "/v1/transit/encrypt/"+keyName, reqBody, &out); err != nil {
		return nil, err
	}
	if out.Data.Ciphertext == "" {
		return nil, fmt.Errorf("vault: empty ciphertext in response")
	}
	// The Vault ciphertext is an opaque "vault:v1:..." string; carry it as bytes.
	return []byte(out.Data.Ciphertext), nil
}

func (c *httpTransitClient) Decrypt(ctx context.Context, keyName string, ciphertext []byte) ([]byte, error) {
	reqBody := map[string]string{"ciphertext": string(ciphertext)}
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := c.do(ctx, "/v1/transit/decrypt/"+keyName, reqBody, &out); err != nil {
		return nil, err
	}
	plaintext, err := base64.StdEncoding.DecodeString(out.Data.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("vault: failed to decode plaintext: %w", err)
	}
	return plaintext, nil
}

func TestProvider_Integration_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Vault integration test in short mode")
	}

	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		t.Skip("Skipping Vault integration test: set VAULT_ADDR (and VAULT_TOKEN) to run")
	}
	token := os.Getenv("VAULT_TOKEN")
	keyName := os.Getenv("MINK_VAULT_TEST_KEY")
	if keyName == "" {
		keyName = "mink-test"
	}

	client := &httpTransitClient{
		addr:  addr,
		token: token,
		hc:    &http.Client{Timeout: 10 * time.Second},
	}

	p := New(WithVaultClient(client))
	defer func() { _ = p.Close() }()

	ctx := context.Background()

	dk, err := p.GenerateDataKey(ctx, keyName)
	require.NoError(t, err)
	require.NotNil(t, dk)
	defer encryption.ClearBytes(dk.Plaintext)

	assert.Len(t, dk.Plaintext, 32)
	assert.NotEmpty(t, dk.Ciphertext)

	plaintext, err := p.DecryptDataKey(ctx, keyName, dk.Ciphertext)
	require.NoError(t, err)
	defer encryption.ClearBytes(plaintext)

	assert.Equal(t, dk.Plaintext, plaintext)
}

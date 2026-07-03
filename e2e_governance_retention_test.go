package mink_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mink "go-mink.dev"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

// newRetentionStore builds a PG store with single-key field encryption for retention tests.
func newRetentionStore(t *testing.T) (*e2ePG, *local.Provider) {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	provider, err := local.New(local.WithKey("k", key))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	p := newE2EPGBase(t)
	p.Store = mink.New(p.Adapter, mink.WithFieldEncryption(mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("k"),
		mink.WithEncryptedFields("e2ePerson", "email"),
		mink.WithDecryptionErrorHandler(func(err error, _ string, _ mink.Metadata) error {
			if errors.Is(err, encryption.ErrKeyRevoked) {
				return nil
			}
			return err
		}),
	)))
	p.Store.RegisterEvents(e2ePerson{})
	return p, provider
}

// TestE2E_Retention_ShredAndDryRunOnPG: a retention Shred policy over a real PG log revokes the
// key, a dry-run changes nothing, and the append-only event rows are never rewritten.
func TestE2E_Retention_ShredAndDryRunOnPG(t *testing.T) {
	p, provider := newRetentionStore(t)
	require.NoError(t, p.Store.Append(p.Ctx, "person-x", []interface{}{e2ePerson{PersonID: "x", Email: "x@example.com"}}))
	require.NoError(t, p.Store.Append(p.Ctx, "person-y", []interface{}{e2ePerson{PersonID: "y", Email: "y@example.com"}}))
	eventsBefore := p.countRows(t, "events")

	mgr := mink.NewRetentionManager(p.Store, []mink.RetentionPolicy{
		{Name: "people", StreamPrefix: "person-", Action: mink.ActionShred},
	})

	// Dry-run reports matches but changes nothing.
	dry, err := mgr.DryRun(p.Ctx)
	require.NoError(t, err)
	assert.True(t, dry.DryRun)
	assert.GreaterOrEqual(t, dry.Matched, 2)
	revoked, err := provider.IsRevoked("k")
	require.NoError(t, err)
	assert.False(t, revoked, "dry-run must not revoke the key")

	// Real run shreds the key.
	rep, err := mgr.Apply(p.Ctx)
	require.NoError(t, err)
	assert.False(t, rep.Failed(), "retention run succeeded: %v", rep.Errors)
	assert.Contains(t, rep.KeysRevoked, "k")
	revoked, err = provider.IsRevoked("k")
	require.NoError(t, err)
	assert.True(t, revoked, "the key is revoked after the sweep")

	// Append-only: no event row was rewritten or removed (shred = key revocation).
	assert.Equal(t, eventsBefore, p.countRows(t, "events"))
}

// TestE2E_Retention_RedactActionAndAnonymizer: a RedactFields policy invokes its Apply hook per
// matched event over a real PG log (append-only preserved), and the Anonymizer is deterministic
// and one-way.
func TestE2E_Retention_RedactActionAndAnonymizer(t *testing.T) {
	p, _ := newRetentionStore(t)
	require.NoError(t, p.Store.Append(p.Ctx, "person-a", []interface{}{e2ePerson{PersonID: "a", Email: "a@example.com"}}))
	require.NoError(t, p.Store.Append(p.Ctx, "person-b", []interface{}{e2ePerson{PersonID: "b", Email: "b@example.com"}}))
	eventsBefore := p.countRows(t, "events")

	var redactedCalls atomic.Int32
	mgr := mink.NewRetentionManager(p.Store, []mink.RetentionPolicy{{
		Name:         "redact-people",
		StreamPrefix: "person-",
		Action:       mink.ActionRedactFields,
		Fields:       []string{"email"},
		Apply: func(_ context.Context, _ mink.StoredEvent) error {
			redactedCalls.Add(1)
			return nil
		},
	}})

	rep, err := mgr.Apply(p.Ctx)
	require.NoError(t, err)
	assert.False(t, rep.Failed(), "%v", rep.Errors)
	assert.GreaterOrEqual(t, int(redactedCalls.Load()), 2, "the Apply hook ran for each matched event")
	assert.Equal(t, eventsBefore, p.countRows(t, "events"), "append-only: redaction does not rewrite the log")

	// Anonymizer: deterministic + one-way + scope-separated.
	a := mink.NewAnonymizer([]byte("secret"))
	p1 := a.Pseudonymize("email", "alice@example.com")
	p2 := a.Pseudonymize("email", "alice@example.com")
	assert.Equal(t, p1, p2, "same (scope,value) -> same pseudonym")
	assert.NotContains(t, p1, "alice", "the original value is not recoverable from the pseudonym")
	assert.NotEqual(t, a.Pseudonymize("name", "x"), a.Pseudonymize("email", "x"), "scope separates pseudonyms")
}

package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mink "go-mink.dev"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

// Security-audit tests for `mink gdpr retain`: the dry-run report must show operators the
// blast radius the shred guard computed (keys to revoke, shared keys refused, plaintext
// matches, keys revoked, errors) using key ids and counts only — never event data.

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it printed.
// The pipe is drained concurrently so output larger than the pipe buffer cannot block.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	return <-done
}

// rawEnvelope is a complete field-encryption envelope under keyID. It is written straight
// through the adapter because the EventStore strips caller-supplied envelope keys on Append.
func rawEnvelope(keyID string) map[string]string {
	return map[string]string{
		"$encrypted_fields":     `["email"]`,
		"$encryption_key_id":    keyID,
		"$encrypted_dek":        "AAAA",
		"$encryption_algorithm": "AES-256-GCM",
	}
}

const retainTestPII = "secret-person@example.com"

// seedRetainStore builds an in-memory store with four events:
//   - Customer-1: ciphertext under k-customer (exclusive to the Customer category)
//   - Customer-2: ciphertext under k-shared
//   - Staff-1:    ciphertext under k-shared, so k-shared is out of scope for Customer
//   - Customer-3: plaintext (no envelope)
func seedRetainStore(t *testing.T) *mink.EventStore {
	t.Helper()
	ctx := context.Background()
	adapter := memory.NewAdapter()
	appendRaw := func(stream string, custom map[string]string) {
		_, err := adapter.Append(ctx, stream, []adapters.EventRecord{{
			Type:     "PersonCreated",
			Data:     []byte(`{"email":"` + retainTestPII + `"}`),
			Metadata: adapters.Metadata{Custom: custom},
		}}, mink.AnyVersion)
		require.NoError(t, err)
	}
	appendRaw("Customer-1", rawEnvelope("k-customer"))
	appendRaw("Customer-2", rawEnvelope("k-shared"))
	appendRaw("Staff-1", rawEnvelope("k-shared"))
	appendRaw("Customer-3", nil)
	return mink.New(adapter)
}

func openSeeded(store *mink.EventStore) gdprStoreOpener {
	return func(context.Context) (*mink.EventStore, func(), error) {
		return store, func() {}, nil
	}
}

func TestGdprRetain_SeededStore_PrintsBlastRadius(t *testing.T) {
	cmd := newGdprRetainCommandWithStore(openSeeded(seedRetainStore(t)))

	var err error
	out := captureStdout(t, func() { err = executeCmd(cmd, []string{"--category", "Customer"}) })
	require.NoError(t, err)

	// Existing lines are unchanged.
	assert.Contains(t, out, "Retention preview (dry-run)")
	assert.Contains(t, out, "Scanned:  4 events")
	assert.Contains(t, out, "Matched:  3 events")
	assert.Contains(t, out, "3 event(s) would be crypto-shredded")

	// New blast-radius lines.
	assert.Contains(t, out, "Unencrypted matches: 1")
	assert.Contains(t, out, "Keys to revoke:      1 [k-customer]")
	assert.Contains(t, out, "Shared keys skipped: 1 [k-shared]")
	assert.Contains(t, out, "Keys revoked:        0 (dry-run: nothing is revoked)")
	assert.Contains(t, out, "Errors:              2")
	assert.Contains(t, out, "1 key(s) refused by the shared-key guard")
	assert.Contains(t, out, "1 matched event(s) carry no encryption envelope")
	assert.Contains(t, out, `refused to revoke key "k-shared"`)
	assert.Contains(t, out, "1 event(s) outside the Shred policy scope")
	assert.Contains(t, out, "remain in plaintext")

	// No PII: payloads and out-of-scope stream ids are never printed.
	assert.NotContains(t, out, retainTestPII)
	assert.NotContains(t, out, "Staff-1")
}

func TestGdprRetain_SeededStore_ExclusiveKeyIsClean(t *testing.T) {
	cmd := newGdprRetainCommandWithStore(openSeeded(seedRetainStore(t)))

	var err error
	out := captureStdout(t, func() { err = executeCmd(cmd, []string{"--prefix", "Customer-1"}) })
	require.NoError(t, err)

	assert.Contains(t, out, "Scanned:  4 events")
	assert.Contains(t, out, "Matched:  1 events")
	assert.Contains(t, out, "Unencrypted matches: 0")
	assert.Contains(t, out, "Keys to revoke:      1 [k-customer]")
	assert.Contains(t, out, "Shared keys skipped: 0")
	assert.Contains(t, out, "Keys revoked:        0")
	assert.Contains(t, out, "Errors:              0")
	assert.NotContains(t, out, "k-shared")
	assert.NotContains(t, out, "refused")
	assert.NotContains(t, out, "remain in plaintext")
	assert.NotContains(t, out, retainTestPII)
}

func TestGdprRetain_SeededStore_NoMatch(t *testing.T) {
	cmd := newGdprRetainCommandWithStore(openSeeded(seedRetainStore(t)))

	var err error
	out := captureStdout(t, func() { err = executeCmd(cmd, []string{"--category", "Nobody"}) })
	require.NoError(t, err)

	assert.Contains(t, out, "No events match this policy")
	assert.Contains(t, out, "Keys to revoke:      0")
	assert.Contains(t, out, "Errors:              0")
	assert.NotContains(t, out, "[k-")
}

func TestGdprRetain_StoreOpenErrorIsReturned(t *testing.T) {
	boom := errors.New("no store for you")
	cmd := newGdprRetainCommandWithStore(func(context.Context) (*mink.EventStore, func(), error) {
		return nil, nil, boom
	})
	err := executeCmd(cmd, []string{"--category", "Customer"})
	assert.ErrorIs(t, err, boom)
}

func TestGdprRetain_DefaultOpenerNeedsConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-gdpr-retain-noconfig-*")
	_ = env // intentionally no mink.yaml

	err := executeCmd(newGdprRetainCommand(), []string{"--category", "Customer"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}

func TestPrintRetentionReport_AppliedSweepShowsEveryField(t *testing.T) {
	report := &mink.RetentionReport{
		Scanned:            10,
		Matched:            4,
		Acted:              2,
		Skipped:            1,
		UnencryptedMatches: 1,
		KeysToRevoke:       []string{"k-a", "k-b"},
		SharedKeysSkipped:  []string{"k-c"},
		KeysRevoked:        []string{"k-a", "k-b"},
		Errors: []error{
			&mink.RetentionSharedKeyError{KeyID: "k-c", OutOfScope: 3},
			mink.ErrRetentionUnencryptedMatches,
		},
	}
	out := captureStdout(t, func() { printRetentionReport(report) })

	assert.Contains(t, out, "Retention sweep")
	assert.NotContains(t, out, "dry-run")
	assert.Contains(t, out, "Scanned:  10 events")
	assert.Contains(t, out, "Matched:  4 events")
	assert.Contains(t, out, "Unencrypted matches: 1")
	assert.Contains(t, out, "Keys to revoke:      2 [k-a, k-b]")
	assert.Contains(t, out, "Shared keys skipped: 1 [k-c]")
	assert.Contains(t, out, "Keys revoked:        2 [k-a, k-b]")
	assert.Contains(t, out, "Errors:              2")
	assert.Contains(t, out, "4 event(s) matched")
	assert.Contains(t, out, "3 event(s) outside the Shred policy scope")
	assert.Contains(t, out, mink.ErrRetentionUnencryptedMatches.Error())
}

func TestPrintRetentionReport_EmptyDryRun(t *testing.T) {
	out := captureStdout(t, func() { printRetentionReport(&mink.RetentionReport{DryRun: true}) })

	assert.Contains(t, out, "Retention preview (dry-run)")
	assert.Contains(t, out, "No events match this policy")
	assert.Contains(t, out, "Unencrypted matches: 0")
	assert.Contains(t, out, "Keys to revoke:      0")
	assert.Contains(t, out, "Shared keys skipped: 0")
	assert.Contains(t, out, "Keys revoked:        0 (dry-run: nothing is revoked)")
	assert.Contains(t, out, "Errors:              0")
	assert.NotContains(t, out, "[")
	assert.NotContains(t, out, "refused")
	assert.NotContains(t, out, "remain in plaintext")
}

func TestCountWithKeyIDs(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want string
	}{
		{"nil", nil, "Keys: 0"},
		{"empty", []string{}, "Keys: 0"},
		{"one", []string{"k-1"}, "Keys: 1 [k-1]"},
		{"many", []string{"k-1", "k-2", "k-3"}, "Keys: 3 [k-1, k-2, k-3]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, countWithKeyIDs("Keys: ", tt.ids))
		})
	}
}

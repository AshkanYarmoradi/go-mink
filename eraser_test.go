package mink

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

type eraseUserCreated struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

// nonJSONMarkerSerializer produces non-JSON Data, so the marker-idempotency test below proves
// the metadata-based (serializer-agnostic) detection works rather than the JSON-Data fallback.
type nonJSONMarkerSerializer struct{}

func (nonJSONMarkerSerializer) Serialize(interface{}) ([]byte, error) {
	return []byte{0x00, 0x01, 0x02, 0xff}, nil
}

func (nonJSONMarkerSerializer) Deserialize([]byte, string) (interface{}, error) {
	return map[string]interface{}{}, nil
}

// TestDataEraser_MarkerIdempotency_NonJSONSerializer verifies the erasure marker is detected
// (and therefore not duplicated on re-run) under a serializer whose Data is not JSON-decodable
// — the metadata subject tag, not the Data, drives markerExists.
func TestDataEraser_MarkerIdempotency_NonJSONSerializer(t *testing.T) {
	ctx := context.Background()
	store := New(memory.NewAdapter(), WithSerializer(nonJSONMarkerSerializer{}))
	eraser := NewDataEraser(store, WithErasureMarker("erasure-log"))
	res := &ErasureResult{KeysRevoked: []string{"k"}}

	require.NoError(t, eraser.appendMarker(ctx, "subj-1", res))
	assert.True(t, eraser.markerExists(ctx, "subj-1"),
		"marker must be detectable via metadata under a non-JSON serializer")

	// Re-run must not append a duplicate — idempotency holds even though Data is not JSON.
	require.NoError(t, eraser.appendMarker(ctx, "subj-1", res))
	raw, err := store.LoadRaw(ctx, "erasure-log", 0)
	require.NoError(t, err)
	assert.Len(t, raw, 1,
		"re-running appendMarker under a non-JSON serializer must not duplicate the marker")
}

// newEraseTestStore builds a memory-backed store with field encryption keyed by
// keyID, plus a crypto-shredding decryption handler so revoked keys redact instead
// of failing. It returns the store and the local provider (to assert revocation).
func newEraseTestStore(t *testing.T, keyID string) (*EventStore, *local.Provider) {
	t.Helper()
	provider, err := local.New(local.WithKey(keyID, make([]byte, 32)))
	require.NoError(t, err)

	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID(keyID),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithDecryptionErrorHandler(func(err error, _ string, _ Metadata) error {
			if errors.Is(err, encryption.ErrKeyRevoked) {
				return nil // crypto-shredded: leave the field redacted
			}
			return err
		}),
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg))
	store.RegisterEvents(eraseUserCreated{}, ErasureMarker{})
	return store, provider
}

// noRevokeProvider implements encryption.Provider but NOT encryption.Revocable.
type noRevokeProvider struct{}

func (noRevokeProvider) Encrypt(context.Context, string, []byte) ([]byte, error) { return nil, nil }
func (noRevokeProvider) Decrypt(context.Context, string, []byte) ([]byte, error) { return nil, nil }
func (noRevokeProvider) GenerateDataKey(context.Context, string) (*encryption.DataKey, error) {
	return nil, nil
}
func (noRevokeProvider) DecryptDataKey(context.Context, string, []byte) ([]byte, error) {
	return nil, nil
}
func (noRevokeProvider) Close() error { return nil }

func TestDataEraser_EraseByStreams(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "user-1-key")
	require.NoError(t, store.Append(ctx, "User-user-1", []interface{}{
		eraseUserCreated{UserID: "user-1", Email: "alice@example.com"},
	}))

	res, err := NewDataEraser(store).Erase(ctx, ErasureRequest{
		SubjectID: "user-1",
		Streams:   []string{"User-user-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"user-1-key"}, res.KeysRevoked)
	assert.Equal(t, []string{"User-user-1"}, res.Streams)
	assert.Equal(t, 1, res.EventsScanned)
	assert.Empty(t, res.Errors)

	revoked, err := provider.IsRevoked("user-1-key")
	require.NoError(t, err)
	assert.True(t, revoked)

	// The email is no longer recoverable as plaintext.
	events, err := store.Load(ctx, "User-user-1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	raw, _ := json.Marshal(events[0].Data)
	assert.NotContains(t, string(raw), "alice@example.com")
}

func TestDataEraser_EraseByFilter(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "user-1-key")
	require.NoError(t, store.Append(ctx, "User-user-1", []interface{}{
		eraseUserCreated{UserID: "user-1", Email: "alice@example.com"},
	}))

	res, err := NewDataEraser(store).Erase(ctx, ErasureRequest{
		SubjectID: "user-1",
		Filter:    FilterByStreamPrefix("User-user-1"),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"user-1-key"}, res.KeysRevoked)

	revoked, _ := provider.IsRevoked("user-1-key")
	assert.True(t, revoked)
}

func TestDataEraser_EraseByKeyIDs(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "user-1-key")

	// No streams/filter: revoke the named key directly (no scan).
	res, err := NewDataEraser(store).Erase(ctx, ErasureRequest{
		SubjectID: "user-1",
		KeyIDs:    []string{"user-1-key"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"user-1-key"}, res.KeysRevoked)
	assert.Equal(t, 0, res.EventsScanned)

	revoked, _ := provider.IsRevoked("user-1-key")
	assert.True(t, revoked)
}

func TestDataEraser_Idempotent(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "user-1-key")
	require.NoError(t, store.Append(ctx, "User-user-1", []interface{}{
		eraseUserCreated{UserID: "user-1", Email: "alice@example.com"},
	}))
	eraser := NewDataEraser(store)
	req := ErasureRequest{SubjectID: "user-1", Streams: []string{"User-user-1"}}

	_, err := eraser.Erase(ctx, req)
	require.NoError(t, err)

	// Re-erasing is a no-op success.
	res, err := eraser.Erase(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, []string{"user-1-key"}, res.KeysRevoked)
	assert.Empty(t, res.Errors)
}

func TestDataEraser_Marker(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "user-1-key")
	require.NoError(t, store.Append(ctx, "User-user-1", []interface{}{
		eraseUserCreated{UserID: "user-1", Email: "alice@example.com"},
	}))

	res, err := NewDataEraser(store, WithErasureMarker("erasure-log")).Erase(ctx, ErasureRequest{
		SubjectID: "user-1",
		Streams:   []string{"User-user-1"},
	})
	require.NoError(t, err)
	assert.True(t, res.MarkerWritten)

	markers, err := store.Load(ctx, "erasure-log")
	require.NoError(t, err)
	require.Len(t, markers, 1)
	raw, _ := json.Marshal(markers[0].Data)
	assert.Contains(t, string(raw), "user-1")
	assert.NotContains(t, string(raw), "alice@example.com")
}

func TestDataEraser_Marker_Idempotent(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "user-1-key")
	require.NoError(t, store.Append(ctx, "User-user-1", []interface{}{
		eraseUserCreated{UserID: "user-1", Email: "alice@example.com"},
	}))

	eraser := NewDataEraser(store, WithErasureMarker("erasure-log"))
	req := ErasureRequest{SubjectID: "user-1", Streams: []string{"User-user-1"}}

	res1, err := eraser.Erase(ctx, req)
	require.NoError(t, err)
	assert.True(t, res1.MarkerWritten)

	// Re-run: revocation is idempotent and the marker must not be duplicated.
	res2, err := eraser.Erase(ctx, req)
	require.NoError(t, err)
	assert.True(t, res2.MarkerWritten)

	markers, err := store.Load(ctx, "erasure-log")
	require.NoError(t, err)
	assert.Len(t, markers, 1, "re-running Erase must not append a duplicate marker")
}

func TestErasureResult_Failed_SkippedSubjectStore(t *testing.T) {
	// A registered subject store that was skipped (unimplemented purger) leaves PII —
	// Failed() must report it even with no Errors/Partial/ResidualReadModels.
	assert.False(t, (&ErasureResult{}).Failed())

	skipped := &ErasureResult{SubjectStores: []SubjectErasureOutcome{{Name: "saga", Skipped: true}}}
	assert.True(t, skipped.Failed(), "a skipped subject store must make Failed() true")

	erased := &ErasureResult{SubjectStores: []SubjectErasureOutcome{{Name: "saga", Erased: 3}}}
	assert.False(t, erased.Failed())
}

func TestDataEraser_Validation(t *testing.T) {
	store, _ := newEraseTestStore(t, "k")
	eraser := NewDataEraser(store)

	_, err := eraser.Erase(context.Background(), ErasureRequest{})
	assert.ErrorIs(t, err, ErrErasureSubjectRequired)

	_, err = eraser.Erase(context.Background(), ErasureRequest{SubjectID: "u"})
	assert.ErrorIs(t, err, ErrNoErasureSources)
}

func TestDataEraser_NotConfigured(t *testing.T) {
	store := New(memory.NewAdapter()) // no field encryption
	_, err := NewDataEraser(store).Erase(context.Background(), ErasureRequest{
		SubjectID: "u", KeyIDs: []string{"k"},
	})
	assert.ErrorIs(t, err, ErrErasureNotConfigured)
}

func TestDataEraser_UnsupportedProvider(t *testing.T) {
	cfg := NewFieldEncryptionConfig(WithEncryptionProvider(noRevokeProvider{}), WithDefaultKeyID("k"))
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg))

	_, err := NewDataEraser(store).Erase(context.Background(), ErasureRequest{
		SubjectID: "u", KeyIDs: []string{"k"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrRevocationUnsupported)
	assert.ErrorIs(t, err, ErrErasureFailed)
}

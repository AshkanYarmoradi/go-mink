package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// userIDTagger tags each event with its UserID metadata as the data subject.
func userIDTagger(_ string, _ []byte, md Metadata) []string {
	if md.UserID == "" {
		return nil
	}
	return []string{md.UserID}
}

func newSubjectTestStore(t *testing.T, keyID string) (*EventStore, *local.Provider) {
	t.Helper()
	provider, err := local.New(local.WithKey(keyID, make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID(keyID),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithDecryptionErrorHandler(func(error, string, Metadata) error { return nil }),
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(eraseUserCreated{}, ErasureMarker{})
	return store, provider
}

func appendUser(t *testing.T, ctx context.Context, store *EventStore, stream, userID string) {
	t.Helper()
	require.NoError(t, store.Append(ctx, stream,
		[]interface{}{eraseUserCreated{UserID: userID, Email: userID + "@example.com"}},
		WithAppendMetadata(Metadata{UserID: userID})))
}

func TestSubjectTagger_RecordsTags(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	raw, err := store.LoadRaw(ctx, "User-u1", 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)
	assert.Equal(t, []string{"u1"}, GetSubjectTags(raw[0].Metadata))
}

func TestSubjectResolver_ScanFootprint(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1")
	appendUser(t, ctx, store, "User-u2", "u2")

	fp, err := NewSubjectResolver(store).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, fp.Streams)
	assert.Equal(t, 2, fp.EventCount)
	assert.Equal(t, []string{"k"}, fp.KeyIDs)
	assert.False(t, fp.Partial)
}

func TestSubjectResolver_PartialOnUntagged(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	// An untagged event (no UserID → tagger returns nil) makes completeness unprovable.
	require.NoError(t, store.Append(ctx, "Sys-1", []interface{}{eraseUserCreated{}}))

	fp, err := NewSubjectResolver(store).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.True(t, fp.Partial)
}

func TestDataEraser_AutoResolveSubject(t *testing.T) {
	ctx := context.Background()
	store, provider := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1")

	eraser := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store)))
	res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, res.KeysRevoked)
	assert.ElementsMatch(t, []string{"Order-o1", "User-u1"}, res.Streams)

	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
}

func TestDataExporter_AutoResolveSubject(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "User-u2", "u2")

	exporter := NewDataExporter(store, WithExportSubjectResolver(NewSubjectResolver(store)))
	res, err := exporter.Export(ctx, ExportRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, 1, res.TotalEvents)
	assert.Equal(t, []string{"User-u1"}, res.Streams)
}

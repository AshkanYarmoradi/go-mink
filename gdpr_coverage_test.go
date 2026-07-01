package mink

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErasure_Options(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	// Exercises WithEraseBatchSize / WithEraseLogger / WithResolverBatchSize.
	eraser := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store, WithResolverBatchSize(250))),
		WithEraseBatchSize(250),
		WithEraseLogger(&noopLogger{}),
	)
	res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Contains(t, res.KeysRevoked, "k")
}

func TestSubjectFilter_MatchesTaggedEvents(t *testing.T) {
	f := SubjectFilter("u1")
	assert.True(t, f(StoredEvent{Metadata: Metadata{Custom: map[string]string{SubjectTagsKey: `["u1","u2"]`}}}))
	assert.False(t, f(StoredEvent{Metadata: Metadata{Custom: map[string]string{SubjectTagsKey: `["u2"]`}}}))
	assert.False(t, f(StoredEvent{}))
}

func TestFieldEncryptionConfig_IsRevoked(t *testing.T) {
	store, provider := newSubjectTestStore(t, "k")
	cfg := store.EncryptionConfig()

	revoked, err := cfg.IsRevoked("k")
	require.NoError(t, err)
	assert.False(t, revoked)

	require.NoError(t, provider.RevokeKey("k"))
	revoked, err = cfg.IsRevoked("k")
	require.NoError(t, err)
	assert.True(t, revoked)
}

func TestRetentionAction_String(t *testing.T) {
	assert.Equal(t, "Shred", ActionShred.String())
	assert.Equal(t, "RedactFields", ActionRedactFields.String())
	assert.Equal(t, "Anonymize", ActionAnonymize.String())
	assert.Equal(t, "Unknown", RetentionAction(99).String())
}

func TestRetention_CategoryAndEventTypeMatchers(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "Customer-c1", []interface{}{
		eraseUserCreated{UserID: "c1", Email: "c1@example.com"},
	}))

	// Category + EventTypes matchers exercise streamCategory + containsString.
	mgr := NewRetentionManager(store, []RetentionPolicy{
		{Name: "cust", Category: "Customer", EventTypes: []string{"eraseUserCreated"}, Action: ActionShred},
	}, WithRetentionBatchSize(250))
	rep, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.Matched)
	assert.Contains(t, rep.KeysRevoked, "k")

	// A non-matching category does nothing.
	mgr2 := NewRetentionManager(store, []RetentionPolicy{
		{Name: "orders", Category: "Order", Action: ActionShred},
	})
	rep2, err := mgr2.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, rep2.Matched)
}

func TestDedupeStrings(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, dedupeStrings([]string{"a", "b", "a", "b", "a"}))
	assert.Equal(t, []string{"x"}, dedupeStrings([]string{"x"}))
	assert.Empty(t, dedupeStrings(nil))
}

func TestExportStream_AutoResolvesSubject(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1")

	// A SubjectID-only ExportStream now auto-resolves (mirrors Export) instead of
	// failing validation.
	exporter := NewDataExporter(store, WithExportSubjectResolver(NewSubjectResolver(store)))
	var streamed int
	err := exporter.ExportStream(ctx, ExportRequest{SubjectID: "u1"}, func(context.Context, ExportedEvent) error {
		streamed++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, streamed)
}

func TestErasureErrorStrings(t *testing.T) {
	assert.Contains(t, NewErasureError("u1", errors.New("boom")).Error(), "u1")

	ske := &SharedKeyError{SubjectID: "u1", SharedKeys: []string{"tenant-A"}, OtherSubjects: []string{"u2"}}
	msg := ske.Error()
	assert.Contains(t, msg, "u1") // the target subject
	assert.Contains(t, msg, "u2") // a co-tenant subject sharing the key
	assert.ErrorIs(t, ske, ErrSharedKeyRevocation)
}

package mink

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// okRedactor is a SubjectRedactable whose RedactSubject always succeeds.
type okRedactor struct{ name string }

func (o okRedactor) ReadModelName() string                     { return o.name }
func (okRedactor) RedactSubject(context.Context, string) error { return nil }

func TestVerify_Guards(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")

	// An empty subject id is rejected up front.
	_, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store))).Verify(ctx, "")
	assert.ErrorIs(t, err, ErrErasureSubjectRequired)

	// Verify needs a resolver to know which streams to check.
	_, err = NewDataEraser(store).Verify(ctx, "u1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolver")
}

func TestDataEraser_EraseTimeWindowFilters(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

	eraser := NewDataEraser(store)
	before := time.Now().Add(time.Hour)
	after := time.Now().Add(-time.Hour)

	// The event predates FromTime → excluded, nothing revoked.
	res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}, FromTime: &before})
	require.NoError(t, err)
	assert.Empty(t, res.KeysRevoked)
	revoked, _ := provider.IsRevoked("k")
	assert.False(t, revoked, "an out-of-window event must not be erased")

	// The event postdates ToTime → excluded.
	res, err = eraser.Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}, ToTime: &after})
	require.NoError(t, err)
	assert.Empty(t, res.KeysRevoked)

	// Within [after, before] → erased.
	res, err = eraser.Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}, FromTime: &after, ToTime: &before})
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, res.KeysRevoked)
}

func TestSetSubjectTags_MergeDedupeAndEmpty(t *testing.T) {
	// Empty ids are skipped, duplicates (including against existing tags) collapse.
	md := setSubjectTags(Metadata{}, []string{"a", "", "a", "b"})
	assert.Equal(t, []string{"a", "b"}, GetSubjectTags(md))

	// Merging new subjects with ones already recorded de-duplicates across both.
	merged := setSubjectTags(md, []string{"b", "c"})
	assert.Equal(t, []string{"a", "b", "c"}, GetSubjectTags(merged))

	// All-empty input leaves the metadata untouched (no tags written).
	unchanged := setSubjectTags(Metadata{}, []string{"", ""})
	assert.Empty(t, GetSubjectTags(unchanged))
}

func TestSampleSet_Truncates(t *testing.T) {
	full := map[string]struct{}{}
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		full[s] = struct{}{}
	}
	assert.Len(t, sampleSet(full, 3), 3, "over-limit sets are truncated")
	assert.Len(t, sampleSet(full, 10), 5, "under-limit sets are returned whole")
}

func TestVerify_ReportsResidualCleartext(t *testing.T) {
	ctx := context.Background()
	// A store WITHOUT field encryption: tagged events carry plaintext PII that cannot be
	// crypto-shredded, so Verify must surface them as residual cleartext and refuse to
	// certify erasure.
	store := New(memory.NewAdapter(), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(eraseUserCreated{})
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "u1@example.com"}},
		WithAppendMetadata(Metadata{UserID: "u1"})))

	rep, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store))).Verify(ctx, "u1")
	require.NoError(t, err)
	assert.NotEmpty(t, rep.ResidualCleartext, "unencrypted PII is residual")
	assert.False(t, rep.Verified, "cleartext PII must block verification")
}

func TestErase_StreamNotFoundIsSkipped(t *testing.T) {
	ctx := context.Background()
	store, provider := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

	// A missing stream in the request is skipped, not fatal; the present one still erases.
	res, err := NewDataEraser(store).Erase(ctx, ErasureRequest{
		SubjectID: "u1", Streams: []string{"User-u1", "Ghost-does-not-exist"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, res.KeysRevoked)
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
}

func TestErase_ScanNotSupported(t *testing.T) {
	provider, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k"),
		WithEncryptedFields("eraseUserCreated", "email"),
	)
	// A filter-based erase must scan the log, which needs a SubscriptionAdapter; the
	// minimal adapter has none, so it fails cleanly instead of silently erasing nothing.
	store := New(&minimalExportAdapter{}, WithFieldEncryption(cfg))
	_, err = NewDataEraser(store).Erase(context.Background(), ErasureRequest{
		SubjectID: "u1", Filter: SubjectFilter("u1"),
	})
	assert.ErrorIs(t, err, ErrErasureScanNotSupported)
}

func TestErase_ReadModelsAndHooks(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	var rebuilt, hooked bool
	eraser := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		// One redactor succeeds, one fails (residual).
		WithReadModelRedactor(okRedactor{name: "profiles"}, failingRedactor{name: "orders"}),
		// One rebuilder succeeds, one fails (residual).
		WithReadModelRebuilder(
			ReadModelRebuilder{Name: "search", Rebuild: func(context.Context) error { rebuilt = true; return nil }},
			ReadModelRebuilder{Name: "broken", Rebuild: func(context.Context) error { return errors.New("rebuild boom") }},
		),
		// One hook succeeds (side effect), one fails.
		WithErasureHook(
			ErasureHook{Name: "crm", Run: func(context.Context, ErasureContext) error { hooked = true; return nil }},
			ErasureHook{Name: "billing", Run: func(context.Context, ErasureContext) error { return errors.New("hook boom") }},
		),
	)

	res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err) // partial-failure contract: non-fatal
	assert.Contains(t, res.KeysRevoked, "k")

	assert.Contains(t, res.RedactedReadModels, "profiles")
	assert.Contains(t, res.RedactedReadModels, "search")
	assert.Contains(t, res.ResidualReadModels, "orders") // failing redactor
	assert.Contains(t, res.ResidualReadModels, "broken") // failing rebuilder
	assert.Contains(t, res.SideEffects, "crm")           // successful hook
	assert.True(t, rebuilt && hooked)
	assert.NotEmpty(t, res.Errors) // the two failures + the failing hook are reported
}

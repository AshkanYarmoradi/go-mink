package mink

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

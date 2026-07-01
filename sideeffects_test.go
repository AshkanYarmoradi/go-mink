package mink

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErase_RunsSideEffectHooks(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

	var seen ErasureContext
	res, err := NewDataEraser(store, WithErasureHook(ErasureHook{
		Name: "blob-storage",
		Run:  func(_ context.Context, ec ErasureContext) error { seen = ec; return nil },
	})).Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"blob-storage"}, res.SideEffects)
	assert.Equal(t, "u1", seen.SubjectID)
	assert.Equal(t, []string{"k"}, seen.KeysRevoked)
}

func TestErase_HookFailureIsNonFatal(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

	res, err := NewDataEraser(store, WithErasureHook(ErasureHook{
		Name: "search-index",
		Run:  func(context.Context, ErasureContext) error { return errors.New("index down") },
	})).Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}})
	require.NoError(t, err) // hook failure is non-fatal
	assert.Empty(t, res.SideEffects)
	assert.NotEmpty(t, res.Errors)
	assert.Equal(t, []string{"k"}, res.KeysRevoked) // erasure still happened
}

func TestSideEffects_OnCertificate(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	var cert ErasureCertificate
	_, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithErasureHook(ErasureHook{Name: "cache", Run: func(context.Context, ErasureContext) error { return nil }}),
		WithCertificateSink(func(_ context.Context, c ErasureCertificate) error { cert = c; return nil }),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"cache"}, cert.SideEffects)
}

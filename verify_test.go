package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerify_DetectsResidualBeforeErase(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	eraser := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store)))
	rep, err := eraser.Verify(ctx, "u1")
	require.NoError(t, err)
	assert.False(t, rep.Verified)
	assert.NotEmpty(t, rep.ResidualEncrypted)
}

func TestVerify_AfterErase(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	eraser := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store)))
	_, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)

	rep, err := eraser.Verify(ctx, "u1")
	require.NoError(t, err)
	assert.True(t, rep.Verified)
	assert.Equal(t, 1, rep.RedactedEvents)
	assert.Empty(t, rep.ResidualEncrypted)
}

func TestErase_EmitsCertificate(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	var got ErasureCertificate
	eraser := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithCertificateSink(func(_ context.Context, c ErasureCertificate) error { got = c; return nil }),
	)
	_, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, "u1", got.SubjectID)
	assert.True(t, got.Verified)
	assert.Equal(t, 1, got.KeysRevoked)
}

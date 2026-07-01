package mink

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDataEraser_SharedKeyGuard_BlocksTenantWideRevoke(t *testing.T) {
	ctx := context.Background()
	store, provider := newSubjectTestStore(t, "shared") // both users under one key
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "User-u2", "u2")

	_, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSharedKeyGuard(),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSharedKeyRevocation)

	var ske *SharedKeyError
	require.True(t, errors.As(err, &ske))
	assert.Contains(t, ske.SharedKeys, "shared")
	assert.Contains(t, ske.OtherSubjects, "u2")

	// Nothing was revoked — the guard fails before the irreversible step.
	revoked, _ := provider.IsRevoked("shared")
	assert.False(t, revoked, "guard must block BEFORE revocation")
}

func TestDataEraser_SharedKeyGuard_OverrideProceeds(t *testing.T) {
	ctx := context.Background()
	store, provider := newSubjectTestStore(t, "shared")
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "User-u2", "u2")

	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSharedKeyGuard(), AllowSharedKeyRevocation(),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Contains(t, res.KeysRevoked, "shared")

	revoked, _ := provider.IsRevoked("shared")
	assert.True(t, revoked)
}

func TestDataEraser_SharedKeyGuard_ExclusiveKeyPasses(t *testing.T) {
	ctx := context.Background()
	store, provider := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1") // only u1 under "k"

	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSharedKeyGuard(),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Contains(t, res.KeysRevoked, "k")

	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
}

func TestDataEraser_StrictAccountability_CertFailureIsFatal(t *testing.T) {
	ctx := context.Background()
	store, provider := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	sinkErr := errors.New("audit store down")
	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithCertificateSink(func(context.Context, ErasureCertificate) error { return sinkErr }),
		WithStrictAccountability(),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrErasureFailed)

	// The (irreversible) revoke still happened — re-running Erase is idempotent.
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
	// The result is returned so the caller sees what happened.
	require.NotNil(t, res)
	assert.True(t, res.Failed())
}

func TestDataEraser_NonStrict_CertFailureIsSoft(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithCertificateSink(func(context.Context, ErasureCertificate) error { return errors.New("down") }),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})

	require.NoError(t, err) // non-fatal by default
	assert.True(t, res.Failed())
	assert.NotEmpty(t, res.Errors)
}

func TestEmitCertificate_GatesVerifiedOnMarker(t *testing.T) {
	ctx := context.Background()
	store, provider := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")
	require.NoError(t, provider.RevokeKey("k")) // subject fully shredded → streams verify clean

	var got ErasureCertificate
	e := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithErasureMarker("erasure-log"),
		WithCertificateSink(func(_ context.Context, c ErasureCertificate) error { got = c; return nil }),
	)
	// A completed revoke whose marker was NOT written must not yield a "verified" cert.
	result := &ErasureResult{SubjectID: "u1", Streams: []string{"User-u1"}, MarkerWritten: false}
	require.NoError(t, e.emitCertificate(ctx, "u1", result))
	assert.False(t, got.Verified, "certificate must not be verified when the marker was not written")
}

func TestDataEraser_ReconcileCatchesLateKey(t *testing.T) {
	ctx := context.Background()
	store, provider := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	e := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store)))
	cfg := store.EncryptionConfig()

	// Simulate a discovery that missed key "k" (empty keySet). Reconcile must find it
	// via re-resolution, revoke it, and flag the result Partial.
	keySet := map[string]struct{}{}
	result := &ErasureResult{SubjectID: "u1"}
	e.reconcileAfterRevoke(ctx, "u1", cfg, keySet, result)

	assert.True(t, result.Partial, "a footprint that grew during erasure must be Partial")
	assert.Contains(t, result.KeysRevoked, "k")
	assert.NotEmpty(t, result.Errors)
	revoked, _ := provider.IsRevoked("k")
	assert.True(t, revoked)
}

// fakeSubjectStore is a test double for SubjectErasable.
type fakeSubjectStore struct {
	name   string
	erased int
	err    error
	gotFP  *SubjectFootprint
}

func (f *fakeSubjectStore) ErasableName() string { return f.name }
func (f *fakeSubjectStore) EraseSubject(_ context.Context, _ string, fp *SubjectFootprint) (SubjectErasureOutcome, error) {
	f.gotFP = fp
	if f.err != nil {
		return SubjectErasureOutcome{Name: f.name}, f.err
	}
	return SubjectErasureOutcome{Name: f.name, Erased: f.erased}, nil
}

func TestDataEraser_SubjectStores(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	ok := &fakeSubjectStore{name: "audit", erased: 3}
	bad := &fakeSubjectStore{name: "saga", err: errors.New("boom")}
	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(ok, bad),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err) // per-store failure is non-fatal

	require.Len(t, res.SubjectStores, 2)
	assert.Equal(t, 3, res.SubjectStores[0].Erased)
	assert.Contains(t, ok.gotFP.Streams, "User-u1", "sibling store receives the footprint streams")
	assert.Equal(t, "boom", res.SubjectStores[1].Err)
	assert.True(t, res.Failed())
}

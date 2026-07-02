package mink

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
)

// failingRevokeProvider is Revocable but its RevokeKey always fails (e.g. a Vault
// Transit key without deletion_allowed) — a partial failure, not ErrRevocationUnsupported.
type failingRevokeProvider struct{ noRevokeProvider }

func (failingRevokeProvider) RevokeKey(string) error {
	return errors.New("vault: deletion_allowed=false")
}
func (failingRevokeProvider) IsRevoked(string) (bool, error) { return false, nil }

func TestDataEraser_RevokeFailureIsNonFatalButFlagged(t *testing.T) {
	cfg := NewFieldEncryptionConfig(WithEncryptionProvider(failingRevokeProvider{}), WithDefaultKeyID("k"))
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg))

	res, err := NewDataEraser(store).Erase(context.Background(), ErasureRequest{SubjectID: "u", KeyIDs: []string{"k"}})
	require.NoError(t, err, "a revoke failure (e.g. Vault deletion_allowed=false) is non-fatal by contract")
	assert.Empty(t, res.KeysRevoked, "the un-revoked key is NOT reported as revoked")
	assert.True(t, res.Failed(), "Failed() surfaces the partial failure a bare err check would miss")
	assert.NotEmpty(t, res.Errors)
}

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

// failingRedactor is a SubjectRedactable whose RedactSubject always fails.
type failingRedactor struct{ name string }

func (f failingRedactor) ReadModelName() string { return f.name }
func (f failingRedactor) RedactSubject(context.Context, string) error {
	return errors.New("redact failed")
}

func TestErase_CertificateNotVerifiedOnResidualReadModel(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	var got ErasureCertificate
	res, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithReadModelRedactor(failingRedactor{name: "users"}),
		WithCertificateSink(func(_ context.Context, c ErasureCertificate) error { got = c; return nil }),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	require.NotEmpty(t, res.ResidualReadModels)
	assert.False(t, got.Verified, "an un-redactable read model may still serve PII — it must block verification")
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
	erased int64
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

func TestErase_CertificateNotVerifiedWhenSubjectStoreFails(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	var got ErasureCertificate
	bad := &fakeSubjectStore{name: "audit", err: errors.New("audit store down")}
	_, err := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(bad),
		WithCertificateSink(func(_ context.Context, c ErasureCertificate) error { got = c; return nil }),
	).Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.False(t, got.Verified, "certificate must not be verified when a sibling store failed to erase")
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
	assert.Equal(t, int64(3), res.SubjectStores[0].Erased)
	assert.Contains(t, ok.gotFP.Streams, "User-u1", "sibling store receives the footprint streams")
	assert.Equal(t, "boom", res.SubjectStores[1].Err)
	assert.True(t, res.Failed())
}

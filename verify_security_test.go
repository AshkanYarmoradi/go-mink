package mink

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

// softRevokedProvider is a StatefulRevocable provider that reports every key as only
// SOFT-revoked (restorable within a grace window) — what the KMS provider reports for
// a key in PendingDeletion — whatever RevokeKey did.
type softRevokedProvider struct{ *local.Provider }

func (softRevokedProvider) RevocationState(string) (encryption.RevocationState, error) {
	return encryption.SoftRevoked, nil
}
func (softRevokedProvider) IsRevoked(string) (bool, error) { return true, nil }

func newSoftRevokedStore(t *testing.T) *EventStore {
	t.Helper()
	inner, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(softRevokedProvider{inner}),
		WithDefaultKeyID("k"),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithDecryptionErrorHandler(func(error, string, Metadata) error { return nil }),
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(eraseUserCreated{}, ErasureMarker{})
	return store
}

// countErrStore is a SubjectErasable whose residual count fails hard.
type countErrStore struct{ err error }

func (countErrStore) ErasableName() string { return "flaky" }
func (countErrStore) EraseSubject(context.Context, string, *SubjectFootprint) (SubjectErasureOutcome, error) {
	return SubjectErasureOutcome{Name: "flaky"}, nil
}
func (c countErrStore) CountSubjectResidual(context.Context, string, *SubjectFootprint) (int64, error) {
	return 0, c.err
}

func captureCert(dst *ErasureCertificate) DataEraserOption {
	return WithCertificateSink(func(_ context.Context, c ErasureCertificate) error { *dst = c; return nil })
}

func notesContain(notes []string, substr string) bool {
	for _, n := range notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

func TestEmitCertificate_KeyIDsOnlyErasureIsVacuous(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	var cert ErasureCertificate
	res, err := NewDataEraser(store, captureCert(&cert)).Erase(ctx, ErasureRequest{SubjectID: "u1", KeyIDs: []string{"k"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, res.KeysRevoked)
	assert.False(t, res.Failed(), "the revocation itself succeeded")

	assert.Equal(t, 0, cert.EventsChecked)
	assert.False(t, cert.Verified, "a certificate that checked no events must not attest the erasure")
	assert.True(t, notesContain(cert.Notes, "no subject-tagged events could be verified"), "%v", cert.Notes)
}

func TestEmitCertificate_EmptyFootprintIsNothingToErase(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")

	var cert ErasureCertificate
	res, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store)), captureCert(&cert)).
		Erase(ctx, ErasureRequest{SubjectID: "ghost"})
	require.NoError(t, err)
	assert.Empty(t, res.KeysRevoked)
	assert.Empty(t, res.Streams)

	assert.True(t, cert.Verified, "nothing resolved and nothing revoked is an honest no-op")
	assert.True(t, notesContain(cert.Notes, "nothing to erase"), "%v", cert.Notes)
}

func TestEmitCertificate_KeyRevocationFailureIsNotVerified(t *testing.T) {
	ctx := context.Background()
	cfg := NewFieldEncryptionConfig(WithEncryptionProvider(failingRevokeProvider{}), WithDefaultKeyID("k"))
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg))

	var cert ErasureCertificate
	res, err := NewDataEraser(store, captureCert(&cert)).Erase(ctx, ErasureRequest{SubjectID: "u1", KeyIDs: []string{"k"}})
	require.NoError(t, err, "a revoke failure is non-fatal by contract")
	assert.Equal(t, []string{"k"}, res.KeysFailed, "the failed key is reported explicitly")
	assert.Empty(t, res.KeysRevoked)
	assert.True(t, res.Failed())

	assert.False(t, cert.Verified, "data under an un-revoked key is recoverable; the certificate must not attest")
	assert.Equal(t, 0, cert.KeysRevoked)
	assert.True(t, notesContain(cert.Notes, "1 key(s) failed to revoke"), "%v", cert.Notes)
}

func TestVerify_SoftRevokedKey_IsRecoverableNotErased(t *testing.T) {
	ctx := context.Background()
	store := newSoftRevokedStore(t)
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1")

	var cert ErasureCertificate
	eraser := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store)), captureCert(&cert))
	res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, res.KeysRevoked, "the provider accepted the revoke call")

	// The provider reports the key as only soft-revoked (PendingDeletion): the PII
	// can still be restored, so neither the certificate nor Verify may attest erasure.
	assert.False(t, cert.Verified)
	assert.Equal(t, 2, cert.EventsChecked)
	assert.True(t, notesContain(cert.Notes, "2 event(s) remain recoverable"), "%v", cert.Notes)
	for _, n := range cert.Notes {
		assert.NotContains(t, n, "User-u1", "notes carry counts, never stream ids")
		assert.NotContains(t, n, "@", "notes carry counts, never stream@version refs")
	}

	rep, err := eraser.Verify(ctx, "u1")
	require.NoError(t, err)
	assert.False(t, rep.Verified)
	assert.Len(t, rep.ResidualRecoverable, 2)
	assert.Equal(t, 0, rep.RedactedEvents)
	assert.True(t, notesContain(rep.Notes, "2 event(s) remain recoverable"), "%v", rep.Notes)
}

func TestVerify_SiblingStoreResidualsBlockVerification(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	outbox := memory.NewOutboxStore()
	require.NoError(t, outbox.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "User-u1", EventType: "E", Destination: "webhook:x", Payload: []byte(`{"email":"u1@example.com"}`)},
	}))

	var cert ErasureCertificate
	eraser := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(NewOutboxSubjectEraser(outbox)),
		captureCert(&cert),
	)

	// Before erasure: the outbox still holds the subject's row.
	rep, err := eraser.Verify(ctx, "u1")
	require.NoError(t, err)
	assert.False(t, rep.Verified)
	assert.Equal(t, []SubjectStoreResidual{{Name: "outbox", Count: 1}}, rep.ResidualStores)
	assert.Empty(t, rep.UncheckedStores)
	assert.True(t, notesContain(rep.Notes, `sibling store "outbox" still holds 1 row(s)`), "%v", rep.Notes)

	res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.False(t, res.Failed(), "%v", res.Errors)
	assert.True(t, cert.Verified)
	assert.Equal(t, []string{"outbox"}, cert.StoresVerified)
	assert.Empty(t, cert.StoresUnchecked)

	// After erasure: events shredded AND the sibling store proven clean.
	rep, err = eraser.Verify(ctx, "u1")
	require.NoError(t, err)
	assert.True(t, rep.Verified)
	assert.Empty(t, rep.ResidualStores)

	// A residual that reappears (e.g. a late outbox write) is caught again.
	require.NoError(t, outbox.Schedule(ctx, []*adapters.OutboxMessage{
		{AggregateID: "User-u1", EventType: "E", Destination: "webhook:x", Payload: []byte(`{}`)},
	}))
	rep, err = eraser.Verify(ctx, "u1")
	require.NoError(t, err)
	assert.False(t, rep.Verified, "a residual sibling-store row must fail verification even though every event is shredded")
}

func TestVerify_UncountableStoresAreReportedUnchecked(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	inner := memory.NewOutboxStore()
	legacy := legacyOutboxStore{OutboxStore: inner, inner: inner} // no counter extension
	custom := &fakeSubjectStore{name: "custom", erased: 1}        // no SubjectResidualCounter at all

	var cert ErasureCertificate
	eraser := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(NewOutboxSubjectEraser(legacy), custom),
		captureCert(&cert),
	)
	rep, err := eraser.Verify(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"outbox", "custom"}, rep.UncheckedStores)
	assert.Empty(t, rep.ResidualStores)
	assert.True(t, notesContain(rep.Notes, "2 sibling store(s) could not be counted"), "%v", rep.Notes)

	_, err = eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"outbox", "custom"}, cert.StoresUnchecked, "the certificate names what it does NOT attest")
	assert.Empty(t, cert.StoresVerified)
	assert.True(t, cert.Verified, "unchecked stores are disclosed, not silently failed")
}

func TestVerify_ResidualCountFailureIsAnError(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	flaky := countErrStore{err: errors.New("db down")}
	var cert ErasureCertificate
	eraser := NewDataEraser(store,
		WithEraseSubjectResolver(NewSubjectResolver(store)),
		WithSubjectStore(flaky),
		captureCert(&cert),
	)
	_, err := eraser.Verify(ctx, "u1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrErasureFailed)
	assert.Contains(t, err.Error(), `"flaky"`)

	res, err := eraser.Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err, "a count failure at certificate time is non-fatal")
	assert.False(t, cert.Verified)
	assert.True(t, notesContain(cert.Notes, "residual count failed"), "%v", cert.Notes)
	assert.True(t, res.Failed())
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e.Error(), "certificate residual count") {
			found = true
		}
	}
	assert.True(t, found, "the count failure is recorded on the result: %v", res.Errors)
}

func TestEmitCertificate_CleartextEventsAreNoted(t *testing.T) {
	ctx := context.Background()
	// Encryption is configured, but for a different event type: the subject's events
	// are tagged yet carry no field encryption, so there is no key to shred.
	provider, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(WithEncryptionProvider(provider), WithDefaultKeyID("k"), WithEncryptedFields("somethingElse", "x"))
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger))
	store.RegisterEvents(eraseUserCreated{})
	appendUser(t, ctx, store, "User-u1", "u1")
	appendUser(t, ctx, store, "Order-o1", "u1")

	fp, err := NewSubjectResolver(store).Resolve(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, 2, fp.CleartextEvents, "the resolver counts cleartext events in the footprint")

	var cert ErasureCertificate
	res, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store)), captureCert(&cert)).
		Erase(ctx, ErasureRequest{SubjectID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, 2, res.CleartextEvents)
	assert.Empty(t, res.KeysRevoked)
	assert.False(t, res.Failed(), "cleartext events do not change Failed(); the erasure did what it could")
	assert.False(t, cert.Verified, "readable PII remains")
	assert.True(t, notesContain(cert.Notes, "2 subject event(s) are not field-encrypted"), "%v", cert.Notes)

	rep, err := NewDataEraser(store, WithEraseSubjectResolver(NewSubjectResolver(store))).Verify(ctx, "u1")
	require.NoError(t, err)
	assert.Len(t, rep.ResidualCleartext, 2)
	assert.True(t, notesContain(rep.Notes, "2 subject event(s) are not field-encrypted"), "%v", rep.Notes)
}

type plainNote struct {
	Text string `json:"text"`
}

func TestErase_StreamsScope_CountsCleartextEvents(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k") // encrypts eraseUserCreated.email only
	store.RegisterEvents(plainNote{})
	require.NoError(t, store.Append(ctx, "User-u1", []interface{}{
		eraseUserCreated{UserID: "u1", Email: "a@b.c"},
		plainNote{Text: "unencrypted"},
	}))

	res, err := NewDataEraser(store).Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, res.KeysRevoked)
	assert.Equal(t, 2, res.EventsScanned)
	assert.Equal(t, 1, res.CleartextEvents, "the plaintext event is counted from the data already loaded")
}

func TestEmitCertificate_EventVerificationFailureIsRecorded(t *testing.T) {
	ctx := context.Background()
	store, _ := newSubjectTestStore(t, "k")
	appendUser(t, ctx, store, "User-u1", "u1")

	var cert ErasureCertificate
	e := NewDataEraser(store, captureCert(&cert))
	// An empty stream id makes LoadRaw fail with a non-NotFound error.
	result := &ErasureResult{SubjectID: "u1", Streams: []string{""}, KeysRevoked: []string{"k"}}
	_, err := e.emitCertificate(ctx, "u1", result)
	require.NoError(t, err, "the sink itself succeeded")
	assert.False(t, cert.Verified)
	assert.True(t, notesContain(cert.Notes, "event verification could not be completed"), "%v", cert.Notes)
	require.NotEmpty(t, result.Errors)
	assert.Contains(t, result.Errors[0].Error(), "certificate event verification")
}

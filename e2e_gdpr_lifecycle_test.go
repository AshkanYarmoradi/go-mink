package mink_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mink "go-mink.dev"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

// e2ePerson carries an encrypted PII field (Email).
type e2ePerson struct {
	PersonID string `json:"personId"`
	Email    string `json:"email"`
}

// e2ePeopleReadModel is a SubjectRedactable read model that records which subjects were redacted.
type e2ePeopleReadModel struct {
	mu       sync.Mutex
	redacted []string
}

func (r *e2ePeopleReadModel) RedactSubject(_ context.Context, subjectID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.redacted = append(r.redacted, subjectID)
	return nil
}
func (r *e2ePeopleReadModel) ReadModelName() string { return "e2e-people" }
func (r *e2ePeopleReadModel) wasRedacted(subjectID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return contains(r.redacted, subjectID)
}

// newGDPRStore builds a PostgreSQL store with field encryption (per-subject keys or one shared
// key), subject tagging, and a subject index — the setup a GDPR erasure needs.
func newGDPRStore(t *testing.T, perSubjectKeys bool) (*e2ePG, *local.Provider, *mink.MemorySubjectIndex) {
	t.Helper()
	provider, err := local.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	encOpts := []mink.EncryptionOption{
		mink.WithEncryptionProvider(provider),
		mink.WithEncryptedFields("e2ePerson", "email"),
		mink.WithDecryptionErrorHandler(func(err error, _ string, _ mink.Metadata) error {
			if errors.Is(err, encryption.ErrKeyRevoked) {
				return nil // crypto-shred: deliver the event with its field left as (unrecoverable) stored
			}
			return err
		}),
	}
	if perSubjectKeys {
		encOpts = append(encOpts, mink.WithSubjectKeyResolver(func(s string) string { return "key-" + s }))
	} else {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		require.NoError(t, provider.AddKey("shared-key", key))
		encOpts = append(encOpts, mink.WithDefaultKeyID("shared-key"))
	}

	tagger := func(_ string, _ []byte, md mink.Metadata) []string {
		if md.UserID != "" {
			return []string{md.UserID}
		}
		return nil
	}
	idx := mink.NewMemorySubjectIndex()

	p := newE2EPGBase(t)
	p.Store = mink.New(p.Adapter,
		mink.WithFieldEncryption(mink.NewFieldEncryptionConfig(encOpts...)),
		mink.WithSubjectTagger(tagger),
		mink.WithSubjectIndexWriter(idx),
	)
	p.Store.RegisterEvents(e2ePerson{}, mink.ErasureMarker{})
	return p, provider, idx
}

// addSubjectKey loads a fresh per-subject master key into the provider.
func addSubjectKey(t *testing.T, provider *local.Provider, subjectID string) {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	require.NoError(t, provider.AddKey("key-"+subjectID, key))
}

// appendPerson appends one encrypted, subject-tagged event for subjectID.
func appendPerson(t *testing.T, p *e2ePG, subjectID, email string) {
	t.Helper()
	require.NoError(t, p.Store.Append(p.Ctx, "person-"+subjectID,
		[]interface{}{e2ePerson{PersonID: subjectID, Email: email}},
		mink.WithAppendMetadata(mink.Metadata{UserID: subjectID}),
	))
}

// loadedEmail loads the first event of a person's stream and returns the email field as loaded
// (plaintext if decryptable, ciphertext if the key was shredded).
func loadedEmail(t *testing.T, p *e2ePG, subjectID string) string {
	t.Helper()
	events, err := p.Store.Load(p.Ctx, "person-"+subjectID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	b, err := json.Marshal(events[0].Data)
	require.NoError(t, err)
	var m map[string]string
	require.NoError(t, json.Unmarshal(b, &m))
	return m["email"]
}

// TestE2E_GDPR_EraseOnPostgres: the full Article 17 erasure flow against real PostgreSQL.
func TestE2E_GDPR_EraseOnPostgres(t *testing.T) {
	p, provider, idx := newGDPRStore(t, true)
	addSubjectKey(t, provider, "alice")
	addSubjectKey(t, provider, "bob")
	appendPerson(t, p, "alice", "alice@example.com")
	appendPerson(t, p, "bob", "bob@example.com")

	// A snapshot for alice's stream, to exercise a sibling (snapshot) store eraser.
	require.NoError(t, p.Adapter.SaveSnapshot(p.Ctx, "person-alice", 1, []byte(`{"snap":true}`)))

	// Sanity: both decrypt before erasure.
	require.Equal(t, "alice@example.com", loadedEmail(t, p, "alice"))
	require.Equal(t, "bob@example.com", loadedEmail(t, p, "bob"))
	eventsBefore := p.countRows(t, "events")

	rm := &e2ePeopleReadModel{}
	resolver := mink.NewSubjectResolver(p.Store, mink.WithResolverIndex(idx), mink.WithAuthoritativeIndex())
	eraser := mink.NewDataEraser(p.Store,
		mink.WithEraseSubjectResolver(resolver),
		mink.WithErasureMarker("gdpr-markers"),
		mink.WithReadModelRedactor(rm),
		mink.WithSubjectStore(mink.NewSnapshotSubjectEraser(p.Adapter)),
	)

	res, err := eraser.Erase(p.Ctx, mink.ErasureRequest{SubjectID: "alice"})
	require.NoError(t, err)
	assert.False(t, res.Failed(), "erasure fully succeeded: %v", res.Errors)
	assert.Contains(t, res.KeysRevoked, "key-alice", "alice's key was revoked")
	assert.True(t, res.MarkerWritten, "an erasure marker was appended")
	assert.True(t, rm.wasRedacted("alice"), "the read model was redacted for alice")

	// Crypto-shred: alice's email is no longer recoverable; bob's is untouched.
	assert.NotEqual(t, "alice@example.com", loadedEmail(t, p, "alice"), "alice's PII is shredded")
	assert.Equal(t, "bob@example.com", loadedEmail(t, p, "bob"), "another subject's data is unaffected")

	// Sibling snapshot store purged.
	snap, err := p.Adapter.LoadSnapshot(p.Ctx, "person-alice")
	require.NoError(t, err)
	assert.Nil(t, snap, "alice's snapshot was purged by the sibling eraser")

	// Append-only: the raw event rows are unchanged in count (marker added on its own stream).
	assert.Equal(t, eventsBefore+1, p.countRows(t, "events"),
		"only the marker event was appended; no original event row was rewritten/removed")

	// Verify: no recoverable PII remains for alice.
	rep, err := eraser.Verify(p.Ctx, "alice")
	require.NoError(t, err)
	assert.Empty(t, rep.ResidualRecoverable, "verification finds no recoverable PII")
}

// TestE2E_GDPR_EraseIdempotentAppendOnly: re-running Erase is a no-op and never rewrites history.
func TestE2E_GDPR_EraseIdempotentAppendOnly(t *testing.T) {
	p, provider, idx := newGDPRStore(t, true)
	addSubjectKey(t, provider, "carol")
	appendPerson(t, p, "carol", "carol@example.com")

	resolver := mink.NewSubjectResolver(p.Store, mink.WithResolverIndex(idx), mink.WithAuthoritativeIndex())
	eraser := mink.NewDataEraser(p.Store,
		mink.WithEraseSubjectResolver(resolver),
		mink.WithErasureMarker("gdpr-markers"),
	)

	_, err := eraser.Erase(p.Ctx, mink.ErasureRequest{SubjectID: "carol"})
	require.NoError(t, err)
	afterFirst := p.countRows(t, "events")

	// Re-run: idempotent, no duplicate marker, no history rewrite.
	res2, err := eraser.Erase(p.Ctx, mink.ErasureRequest{SubjectID: "carol"})
	require.NoError(t, err)
	assert.False(t, res2.Failed())
	assert.Equal(t, afterFirst, p.countRows(t, "events"), "re-erasure appends no duplicate marker")

	markers, err := p.Store.Load(p.Ctx, "gdpr-markers")
	require.NoError(t, err)
	assert.Len(t, markers, 1, "exactly one erasure marker exists for the subject")
}

// TestE2E_GDPR_SharedKeyGuardRefuses: a key shared across subjects is not shredded unless allowed.
func TestE2E_GDPR_SharedKeyGuardRefuses(t *testing.T) {
	p, _, idx := newGDPRStore(t, false) // one shared key for everyone
	appendPerson(t, p, "dave", "dave@example.com")
	appendPerson(t, p, "erin", "erin@example.com")

	resolver := mink.NewSubjectResolver(p.Store, mink.WithResolverIndex(idx), mink.WithAuthoritativeIndex())
	eraser := mink.NewDataEraser(p.Store,
		mink.WithEraseSubjectResolver(resolver),
		mink.WithSharedKeyGuard(), // refuse to shred a key that protects other subjects
	)

	_, err := eraser.Erase(p.Ctx, mink.ErasureRequest{SubjectID: "dave"})
	require.Error(t, err, "shared-key erasure is refused")
	assert.ErrorIs(t, err, mink.ErrSharedKeyRevocation, "the refusal is a SharedKeyError")

	// The shared key was NOT shredded, so both subjects' data is still recoverable.
	assert.Equal(t, "erin@example.com", loadedEmail(t, p, "erin"), "the co-tenant's data survives")
	assert.Equal(t, "dave@example.com", loadedEmail(t, p, "dave"), "the guarded subject's data is untouched")
}

// TestE2E_GDPR_ExportOnPostgres: DataExporter over a real PG stream, with a shredded event redacted.
func TestE2E_GDPR_ExportOnPostgres(t *testing.T) {
	p, provider, idx := newGDPRStore(t, true)
	addSubjectKey(t, provider, "frank")
	addSubjectKey(t, provider, "gina")
	appendPerson(t, p, "frank", "frank@example.com")
	appendPerson(t, p, "gina", "gina@example.com")

	resolver := mink.NewSubjectResolver(p.Store, mink.WithResolverIndex(idx), mink.WithAuthoritativeIndex())
	exporter := mink.NewDataExporter(p.Store, mink.WithExportSubjectResolver(resolver))

	// Gina's export contains her plaintext.
	ginaExport, err := exporter.Export(p.Ctx, mink.ExportRequest{SubjectID: "gina"})
	require.NoError(t, err)
	require.Len(t, ginaExport.Events, 1)
	assert.False(t, ginaExport.Events[0].Redacted)
	b, _ := json.Marshal(ginaExport.Events[0].Data)
	assert.Contains(t, string(b), "gina@example.com", "live-key subject exports plaintext")

	// Shred frank's key, then export: his event comes back redacted with no data.
	require.NoError(t, provider.RevokeKey("key-frank"))
	frankExport, err := exporter.Export(p.Ctx, mink.ExportRequest{SubjectID: "frank"})
	require.NoError(t, err)
	require.Len(t, frankExport.Events, 1)
	assert.True(t, frankExport.Events[0].Redacted, "crypto-shredded event is redacted in the export")
	assert.Nil(t, frankExport.Events[0].Data, "redacted event carries no decrypted data")
	assert.Equal(t, 1, frankExport.RedactedCount)
}

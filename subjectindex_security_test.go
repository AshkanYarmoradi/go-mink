package mink

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption"
	"go-mink.dev/encryption/local"
)

// emailTagger derives the data subject from the event's (plaintext) email field —
// a tagger that can only work over DECRYPTED data.
func emailTagger(_ string, data []byte, _ Metadata) []string {
	var v struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(data, &v) != nil || v.Email == "" {
		return nil
	}
	return []string{v.Email}
}

// warnLogged reports whether any Warn message captured by l contains substr.
func warnLogged(l *testLogger, substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, msg := range l.warnLogs {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

func TestMemorySubjectIndex_DeleteSubject(t *testing.T) {
	ctx := context.Background()
	idx := NewMemorySubjectIndex()
	require.NoError(t, idx.IndexSubjects(ctx, "User-u1", []string{"u1", "u2"}))
	require.NoError(t, idx.IndexSubjects(ctx, "Order-o1", []string{"u1"}))

	require.NoError(t, idx.DeleteSubject(ctx, "u1"))
	got, err := idx.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Empty(t, got)
	got, err = idx.StreamsBySubject(ctx, "u2")
	require.NoError(t, err)
	assert.Equal(t, []string{"User-u1"}, got, "other subjects keep their entries")

	require.NoError(t, idx.DeleteSubject(ctx, "u1"), "idempotent")
	require.NoError(t, idx.DeleteSubject(ctx, "nobody"), "unknown subject is a no-op")
	require.NoError(t, idx.DeleteSubject(ctx, ""), "empty subject is a no-op")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	assert.ErrorIs(t, idx.DeleteSubject(cancelled, "u2"), context.Canceled)
	got, _ = idx.StreamsBySubject(ctx, "u2")
	assert.Equal(t, []string{"User-u1"}, got, "a cancelled purge changes nothing")

	var _ SubjectIndexPurger = idx
}

func TestBackfillSubjectIndex_DecryptsBeforeTagging(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k") // encrypts eraseUserCreated.email; no tagger
	require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "alice@example.com"}}))
	require.NoError(t, store.Append(ctx, "Note-n1", []interface{}{plainNote{Text: "no email here"}}))

	// Sanity: at rest the email is ciphertext, so a tagger over raw data cannot see it.
	raw, err := store.LoadRaw(ctx, "User-u1", 0)
	require.NoError(t, err)
	require.True(t, IsEncrypted(raw[0].Metadata))
	require.NotContains(t, string(raw[0].Data), "alice@example.com")

	idx := NewMemorySubjectIndex()
	n, err := BackfillSubjectIndex(ctx, store, emailTagger, idx, 10)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	got, err := idx.StreamsBySubject(ctx, "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, []string{"User-u1"}, got, "the tagger ran over the DECRYPTED payload")

	// The original event is untouched (append-only; decryption happened on a copy).
	raw, err = store.LoadRaw(ctx, "User-u1", 0)
	require.NoError(t, err)
	assert.True(t, IsEncrypted(raw[0].Metadata))
}

func TestBackfillSubjectIndex_UndecryptableFallsBackToExistingTags(t *testing.T) {
	ctx := context.Background()

	t.Run("revoked key with a swallowing handler", func(t *testing.T) {
		logger := newTestLogger()
		provider, err := local.New(local.WithKey("k", make([]byte, 32)))
		require.NoError(t, err)
		cfg := NewFieldEncryptionConfig(
			WithEncryptionProvider(provider),
			WithDefaultKeyID("k"),
			WithEncryptedFields("eraseUserCreated", "email"),
			WithDecryptionErrorHandler(func(error, string, Metadata) error { return nil }), // crypto-shred: leave ciphertext
		)
		store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger), WithLogger(logger))
		store.RegisterEvents(eraseUserCreated{})
		appendUser(t, ctx, store, "User-u1", "u1") // tagged ["u1"] at append time, encrypted
		require.NoError(t, provider.RevokeKey("k"))

		idx := NewMemorySubjectIndex()
		n, err := BackfillSubjectIndex(ctx, store, emailTagger, idx, 10)
		require.NoError(t, err)
		assert.Equal(t, 1, n)

		got, _ := idx.StreamsBySubject(ctx, "u1")
		assert.Equal(t, []string{"User-u1"}, got, "the event's existing tag is still indexed")
		got, _ = idx.StreamsBySubject(ctx, "u1@example.com")
		assert.Empty(t, got, "the tagger is NOT run over ciphertext")
		assert.True(t, warnLogged(logger, "could not decrypt"), "a warning with the count is logged: %v", logger.warnLogs)
	})

	t.Run("revoked key without a handler (hard decrypt error)", func(t *testing.T) {
		logger := newTestLogger()
		provider, err := local.New(local.WithKey("k", make([]byte, 32)))
		require.NoError(t, err)
		cfg := NewFieldEncryptionConfig(
			WithEncryptionProvider(provider),
			WithDefaultKeyID("k"),
			WithEncryptedFields("eraseUserCreated", "email"),
		)
		store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithLogger(logger))
		store.RegisterEvents(eraseUserCreated{})
		require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "alice@example.com"}}))
		require.NoError(t, provider.RevokeKey("k"))
		// Sanity: the decrypt path now fails hard.
		raw, err := store.LoadRaw(ctx, "User-u1", 0)
		require.NoError(t, err)
		_, err = store.DecryptStoredEvent(ctx, raw[0])
		require.ErrorIs(t, err, encryption.ErrKeyRevoked)

		idx := NewMemorySubjectIndex()
		n, err := BackfillSubjectIndex(ctx, store, emailTagger, idx, 10)
		require.NoError(t, err, "an undecryptable event does not fail the backfill")
		assert.Equal(t, 1, n)
		got, _ := idx.StreamsBySubject(ctx, "alice@example.com")
		assert.Empty(t, got)
		assert.True(t, warnLogged(logger, "could not decrypt"), "%v", logger.warnLogs)
	})

	t.Run("no encryption configured for an encrypted event", func(t *testing.T) {
		encStore, _ := newEraseTestStore(t, "k")
		require.NoError(t, encStore.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "alice@example.com"}}))

		logger := newTestLogger()
		plain := New(encStore.Adapter(), WithLogger(logger)) // same events, no decrypt capability
		idx := NewMemorySubjectIndex()
		n, err := BackfillSubjectIndex(ctx, plain, emailTagger, idx, 10)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		got, _ := idx.StreamsBySubject(ctx, "alice@example.com")
		assert.Empty(t, got, "ciphertext is never handed to the tagger")
		assert.True(t, warnLogged(logger, "could not decrypt"), "%v", logger.warnLogs)
	})

	t.Run("plaintext store logs nothing", func(t *testing.T) {
		logger := newTestLogger()
		store := New(memory.NewAdapter(), WithLogger(logger))
		store.RegisterEvents(eraseUserCreated{})
		require.NoError(t, store.Append(ctx, "User-u1", []interface{}{eraseUserCreated{UserID: "u1", Email: "alice@example.com"}}))
		idx := NewMemorySubjectIndex()
		_, err := BackfillSubjectIndex(ctx, store, emailTagger, idx, 10)
		require.NoError(t, err)
		got, _ := idx.StreamsBySubject(ctx, "alice@example.com")
		assert.Equal(t, []string{"User-u1"}, got)
		assert.Empty(t, logger.warnLogs)
	})
}

// failingIndexWriter fails every IndexSubjects call.
type failingIndexWriter struct{}

func (failingIndexWriter) IndexSubjects(context.Context, string, []string) error {
	return errors.New("writer down")
}

func TestBackfillSubjectIndex_WriterFailureStillLogsUndecryptable(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()
	provider, err := local.New(local.WithKey("k", make([]byte, 32)))
	require.NoError(t, err)
	cfg := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k"),
		WithEncryptedFields("eraseUserCreated", "email"),
		WithDecryptionErrorHandler(func(error, string, Metadata) error { return nil }),
	)
	store := New(memory.NewAdapter(), WithFieldEncryption(cfg), WithSubjectTagger(userIDTagger), WithLogger(logger))
	store.RegisterEvents(eraseUserCreated{})
	appendUser(t, ctx, store, "User-u1", "u1")
	require.NoError(t, provider.RevokeKey("k"))

	_, err = BackfillSubjectIndex(ctx, store, emailTagger, failingIndexWriter{}, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backfill index stream")
	assert.True(t, warnLogged(logger, "could not decrypt"), "the count is reported even on an early return: %v", logger.warnLogs)
}

package mink

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/encryption"
)

// appendEncryptedProjEvent appends one field-encrypted projEncEvent and asserts it is
// ciphertext at rest, returning the stored form.
func appendEncryptedProjEvent(t *testing.T, store *EventStore, streamID string, ev projEncEvent) StoredEvent {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.Append(ctx, streamID, []interface{}{ev}))
	raw, err := store.LoadRaw(ctx, streamID, 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)
	require.True(t, IsEncrypted(raw[0].Metadata), "precondition: encrypted at rest")
	require.NotContains(t, string(raw[0].Data), ev.Name, "precondition: ciphertext at rest")
	return raw[0]
}

// receivePolled waits for one event from the polling subscription.
func receivePolled(t *testing.T, sub *PollingSubscription) (StoredEvent, bool) {
	t.Helper()
	select {
	case ev, ok := <-sub.Events():
		return ev, ok
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the polling subscription")
		return StoredEvent{}, false
	}
}

func TestPollingSubscription_DecryptsFieldEncryptedEvents(t *testing.T) {
	store, _ := newProjEncStore(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	appendEncryptedProjEvent(t, store, "User-poll-1",
		projEncEvent{UserID: "u1", Name: "Alice", Emails: []string{"a@example.com"}})

	sub := NewPollingSubscription(store, 0)
	sub.Start(ctx, 10*time.Millisecond)
	defer func() { _ = sub.Close() }()

	ev, ok := receivePolled(t, sub)
	require.True(t, ok)

	// Delivered exactly like Load / CatchupSubscription would: plaintext, markers cleared.
	assert.False(t, IsEncrypted(ev.Metadata), "a decrypted event must not still be flagged encrypted")
	var got projEncEvent
	require.NoError(t, json.Unmarshal(ev.Data, &got))
	assert.Equal(t, "Alice", got.Name)
	assert.Equal(t, []string{"a@example.com"}, got.Emails)
	assert.Nil(t, sub.Err())
}

func TestPollingSubscription_FilterMatchesOnPlaintext(t *testing.T) {
	store, _ := newProjEncStore(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	appendEncryptedProjEvent(t, store, "User-poll-2",
		projEncEvent{UserID: "u2", Name: "Carol", Emails: []string{"c@example.com"}})

	opts := DefaultSubscriptionOptions()
	opts.Filter = plaintextNameFilter{name: "Carol"}
	sub := NewPollingSubscription(store, 0, opts)
	sub.Start(ctx, 10*time.Millisecond)
	defer func() { _ = sub.Close() }()

	ev, ok := receivePolled(t, sub)
	require.True(t, ok)
	assert.Contains(t, string(ev.Data), "Carol")
}

// plaintextNameFilter only matches when it can read the plaintext name — so it can
// only ever match a decrypted event.
type plaintextNameFilter struct{ name string }

func (f plaintextNameFilter) Matches(event StoredEvent) bool {
	var v projEncEvent
	if err := json.Unmarshal(event.Data, &v); err != nil {
		return false
	}
	return v.Name == f.name
}

func TestPollingSubscription_HardDecryptErrorStopsSubscription(t *testing.T) {
	store, provider := newProjEncStore(t, nil) // no handler → fail closed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	appendEncryptedProjEvent(t, store, "User-poll-3",
		projEncEvent{UserID: "u3", Name: "Dave", Emails: []string{"d@example.com"}})

	// The key is gone before the subscription reads the event.
	require.NoError(t, provider.Close())

	sub := NewPollingSubscription(store, 0)
	sub.Start(ctx, 10*time.Millisecond)
	defer func() { _ = sub.Close() }()

	// Nothing is delivered — neither ciphertext nor a silently skipped event — and the
	// subscription stops (channel closed) with the decryption error exposed via Err().
	ev, ok := receivePolled(t, sub)
	require.False(t, ok, "no event must be delivered; got %+v", ev)
	require.Error(t, sub.Err())
	assert.ErrorIs(t, sub.Err(), encryption.ErrProviderClosed)
}

func TestPollingSubscription_CryptoShredHandlerDeliversAsStored(t *testing.T) {
	store, provider := newProjEncStore(t, func(error, string, Metadata) error {
		return nil // swallow: the subject was crypto-shredded
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	raw := appendEncryptedProjEvent(t, store, "User-poll-4",
		projEncEvent{UserID: "u4", Name: "Erin", Emails: []string{"e@example.com"}})
	require.NoError(t, provider.Close())

	sub := NewPollingSubscription(store, 0)
	sub.Start(ctx, 10*time.Millisecond)
	defer func() { _ = sub.Close() }()

	ev, ok := receivePolled(t, sub)
	require.True(t, ok)
	// Matches every other read surface: fields left as stored, still flagged encrypted.
	assert.True(t, IsEncrypted(ev.Metadata))
	assert.Equal(t, raw.Data, ev.Data)
	assert.Nil(t, sub.Err())
}

package mink

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
)

// sagaEncEvent mirrors the shape that exposed the bug in production: a scalar encrypted
// field (Name) and a NON-scalar one (Emails). A non-scalar field is stored as a single
// base64 string when encrypted, so unmarshalling a still-encrypted payload into this
// struct fails with "cannot unmarshal string into Go struct field ... of type []string".
// That is precisely how an undecrypted event dead-ends a saga.
type sagaEncEvent struct {
	UserID string   `json:"userId"`
	Name   string   `json:"name"`
	Emails []string `json:"emails"`
}

// encCapturingSaga unmarshals its trigger event into sagaEncEvent and records the result,
// so a test can assert what the saga actually observed — plaintext, ciphertext, or a
// failed unmarshal.
type encCapturingSaga struct {
	SagaBase
	data map[string]interface{}
}

// encSagaSink collects what every constructed saga instance saw. It is package-level
// state owned by each test via reset(), because the manager builds sagas through a
// factory it owns.
type encSagaSink struct {
	mu         sync.Mutex
	got        []sagaEncEvent
	handleErrs []error
	rawData    [][]byte
}

func (s *encSagaSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = nil
	s.handleErrs = nil
	s.rawData = nil
}

func (s *encSagaSink) record(v sagaEncEvent, raw []byte, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, v)
	s.rawData = append(s.rawData, raw)
	s.handleErrs = append(s.handleErrs, err)
}

func (s *encSagaSink) snapshot() ([]sagaEncEvent, [][]byte, []error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sagaEncEvent(nil), s.got...),
		append([][]byte(nil), s.rawData...),
		append([]error(nil), s.handleErrs...)
}

var encSink = &encSagaSink{}

func newEncCapturingSaga(id string) Saga {
	return &encCapturingSaga{SagaBase: NewSagaBase(id, "EncSaga")}
}

func (s *encCapturingSaga) HandledEvents() []string { return []string{"sagaEncEvent"} }

func (s *encCapturingSaga) HandleEvent(_ context.Context, event StoredEvent) ([]Command, error) {
	var v sagaEncEvent
	err := json.Unmarshal(event.Data, &v)
	encSink.record(v, event.Data, err)
	if err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *encCapturingSaga) Compensate(context.Context, int, error) ([]Command, error) {
	return nil, nil
}
func (s *encCapturingSaga) IsComplete() bool                 { return true }
func (s *encCapturingSaga) Data() map[string]interface{}     { return s.data }
func (s *encCapturingSaga) SetData(d map[string]interface{}) { s.data = d }

// newEncSagaManager builds an event store with field encryption over sagaEncEvent's
// name+emails, plus a saga manager with encCapturingSaga registered. handler, when
// non-nil, installs a DecryptionErrorHandler.
func newEncSagaManager(t *testing.T, handler func(error, string, Metadata) error) (*EventStore, *SagaManager, *local.Provider) {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	provider, err := local.New(local.WithKey("k", key))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	opts := []EncryptionOption{
		WithEncryptionProvider(provider),
		WithDefaultKeyID("k"),
		WithEncryptedFields("sagaEncEvent", "name", "emails"),
	}
	if handler != nil {
		opts = append(opts, WithDecryptionErrorHandler(handler))
	}
	store := New(memory.NewAdapter(), WithFieldEncryption(NewFieldEncryptionConfig(opts...)))
	store.RegisterEvents(sagaEncEvent{})

	return store, newManagerFor(t, store), provider
}

func newManagerFor(t *testing.T, store *EventStore) *SagaManager {
	t.Helper()
	manager := NewSagaManager(store,
		WithSagaStore(newMockSagaStore()),
		WithCommandBus(NewCommandBus()),
	)
	manager.Register("EncSaga", newEncCapturingSaga, SagaCorrelation{
		SagaType:       "EncSaga",
		StartingEvents: []string{"sagaEncEvent"},
		CorrelationIDFunc: func(event StoredEvent) string {
			return event.StreamID
		},
	})
	return manager
}

// appendAndLoadRaw appends one event and returns it exactly as stored — the same bytes
// the raw adapter subscription (Adapter().SubscribeAll) hands the saga manager.
func appendAndLoadRaw(t *testing.T, store *EventStore, streamID string, ev sagaEncEvent) StoredEvent {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.Append(ctx, streamID, []interface{}{ev}))
	raw, err := store.LoadRaw(ctx, streamID, 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)
	return raw[0]
}

// TestSagaManager_ProcessEvent_DecryptsFieldEncryptedEvent is the regression test for the
// saga manager subscribing through the RAW adapter and therefore bypassing the store's
// transparent field decryption. Before the fix the saga was handed ciphertext and its
// json.Unmarshal failed, silently dead-ending every saga started by an encrypted event.
func TestSagaManager_ProcessEvent_DecryptsFieldEncryptedEvent(t *testing.T) {
	encSink.reset()
	ctx := context.Background()
	store, manager, _ := newEncSagaManager(t, nil)

	want := sagaEncEvent{UserID: "u1", Name: "Alice", Emails: []string{"a@example.com", "alt@example.com"}}
	stored := appendAndLoadRaw(t, store, "U-1", want)

	// Precondition: the as-stored event really is encrypted — otherwise this test would
	// pass vacuously against a store that never encrypted anything.
	require.True(t, IsEncrypted(stored.Metadata), "event should be encrypted at rest")
	require.NotContains(t, string(stored.Data), "Alice")

	require.NoError(t, manager.ProcessEvent(ctx, stored))

	got, _, errs := encSink.snapshot()
	require.Len(t, got, 1, "saga should have been driven exactly once")
	require.NoError(t, errs[0], "saga must not see ciphertext it cannot unmarshal")
	assert.Equal(t, want, got[0], "saga must observe the same plaintext Load/projections see")
}

// TestSagaManager_ProcessEvent_Unencrypted_Unchanged pins the zero-overhead invariant:
// a store with no encryption configured behaves exactly as before.
func TestSagaManager_ProcessEvent_Unencrypted_Unchanged(t *testing.T) {
	encSink.reset()
	ctx := context.Background()

	store := New(memory.NewAdapter())
	store.RegisterEvents(sagaEncEvent{})
	manager := newManagerFor(t, store)

	want := sagaEncEvent{UserID: "u2", Name: "Bob", Emails: []string{"b@example.com"}}
	stored := appendAndLoadRaw(t, store, "U-2", want)
	require.False(t, IsEncrypted(stored.Metadata))

	require.NoError(t, manager.ProcessEvent(ctx, stored))

	got, _, errs := encSink.snapshot()
	require.Len(t, got, 1)
	require.NoError(t, errs[0])
	assert.Equal(t, want, got[0])
}

// TestSagaManager_ProcessEvent_UnhandledEventTypeSkipsDecrypt pins the
// decrypt-only-what-is-handled optimization: an event no saga handles is never decrypted
// (and never reaches a saga), so an encrypted event for an unregistered type costs
// nothing and cannot fail.
func TestSagaManager_ProcessEvent_UnhandledEventTypeSkipsDecrypt(t *testing.T) {
	encSink.reset()
	ctx := context.Background()
	store, manager, provider := newEncSagaManager(t, nil)

	stored := appendAndLoadRaw(t, store, "U-3",
		sagaEncEvent{UserID: "u3", Name: "Carol", Emails: []string{"c@example.com"}})

	// Retype the event to something no saga handles. Closing the provider would make any
	// decryption attempt fail, so a nil error proves decryption was never attempted.
	stored.Type = "NobodyHandlesThis"
	require.NoError(t, provider.Close())

	require.NoError(t, manager.ProcessEvent(ctx, stored))

	got, _, _ := encSink.snapshot()
	assert.Empty(t, got, "no saga handles this type, so none should be driven")
}

// TestSagaManager_ProcessEvent_CryptoShreddedDeliveredAsStored covers the erasure case:
// when the configured DecryptionErrorHandler swallows the error (the crypto-shred
// policy), the event is delivered with its fields left as stored and no error — the same
// contract every other read surface honors. The saga still runs; it simply cannot read
// the shredded fields.
func TestSagaManager_ProcessEvent_CryptoShreddedDeliveredAsStored(t *testing.T) {
	encSink.reset()
	ctx := context.Background()

	shredded := errors.New("key revoked")
	store, manager, provider := newEncSagaManager(t, func(error, string, Metadata) error {
		return nil // swallow: subject was crypto-shredded
	})

	stored := appendAndLoadRaw(t, store, "U-4",
		sagaEncEvent{UserID: "u4", Name: "Dave", Emails: []string{"d@example.com"}})

	// Make decryption fail the way a revoked key would.
	require.NoError(t, provider.Close())
	_ = shredded

	// The handler swallows the failure, so ProcessEvent itself does not error.
	require.NoError(t, manager.ProcessEvent(ctx, stored))

	_, raw, errs := encSink.snapshot()
	require.Len(t, errs, 1, "saga should still have been driven")
	// The saga saw the event as stored: its unmarshal of the still-encrypted non-scalar
	// field fails, which is the expected, visible outcome of an unrecoverable field.
	require.Error(t, errs[0])
	assert.NotContains(t, string(raw[0]), "Dave")
}

// TestSagaManager_ProcessEvent_HardDecryptErrorSurfaces pins that a hard, unhandled
// decryption failure is reported rather than silently handing sagas unparseable
// ciphertext — the failure mode that made the original bug invisible.
func TestSagaManager_ProcessEvent_HardDecryptErrorSurfaces(t *testing.T) {
	encSink.reset()
	ctx := context.Background()
	store, manager, provider := newEncSagaManager(t, nil) // no handler → fail closed

	stored := appendAndLoadRaw(t, store, "U-5",
		sagaEncEvent{UserID: "u5", Name: "Erin", Emails: []string{"e@example.com"}})

	require.NoError(t, provider.Close())

	err := manager.ProcessEvent(ctx, stored)
	require.Error(t, err, "an undecryptable event must not be delivered silently")
	assert.Contains(t, err.Error(), "saga delivery")

	got, _, _ := encSink.snapshot()
	assert.Empty(t, got, "no saga should be driven with ciphertext it cannot parse")
}

// TestSagaManager_decryptEvent_NilEventStore pins the nil-guard: a manager built without
// an event store (tests and callers driving ProcessEvent directly) passes events through
// untouched rather than panicking.
func TestSagaManager_decryptEvent_NilEventStore(t *testing.T) {
	manager := NewSagaManager(nil)
	in := StoredEvent{ID: "e1", Type: "sagaEncEvent", Data: []byte(`{"userId":"u"}`)}

	out, err := manager.decryptEvent(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

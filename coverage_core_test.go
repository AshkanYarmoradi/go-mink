package mink

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters/memory"
)

// (The binary-serializer / adapter compatibility guard is covered by serializer_compat_test.go.)

// --- isShutdownError (saga_manager.go) ---

func TestIsShutdownError(t *testing.T) {
	assert.True(t, isShutdownError(context.Canceled))
	assert.True(t, isShutdownError(context.DeadlineExceeded))
	assert.False(t, isShutdownError(errors.New("business failure")))
	assert.False(t, isShutdownError(nil))
}

// --- stripEncryptionMetadata (encryption.go) ---

func TestStripEncryptionMetadata(t *testing.T) {
	// nil Custom is returned unchanged.
	out := stripEncryptionMetadata(Metadata{})
	assert.Nil(t, out.Custom)

	md := Metadata{Custom: map[string]string{
		encryptedFieldsKey:     `["email"]`,
		encryptionKeyIDKey:     "k",
		encryptedDEKKey:        "dek",
		encryptionAlgorithmKey: "AES-256-GCM",
		"$subjects":            `["u1"]`,
		"tenant":               "t1",
	}}
	out = stripEncryptionMetadata(md)
	assert.NotContains(t, out.Custom, encryptedFieldsKey)
	assert.NotContains(t, out.Custom, encryptionKeyIDKey)
	assert.NotContains(t, out.Custom, encryptedDEKKey)
	assert.NotContains(t, out.Custom, encryptionAlgorithmKey)
	assert.Equal(t, `["u1"]`, out.Custom["$subjects"], "non-encryption keys are preserved")
	assert.Equal(t, "t1", out.Custom["tenant"])
	// Input is not mutated.
	assert.Contains(t, md.Custom, encryptedFieldsKey)
}

// --- validateFilterFields (repository.go) ---

type covFilterModel struct {
	ID    string
	Email string
}

func TestValidateFilterFields(t *testing.T) {
	// Empty filters are always valid.
	assert.NoError(t, validateFilterFields[covFilterModel](nil))
	// Non-struct T short-circuits to nil.
	assert.NoError(t, validateFilterFields[int]([]Filter{{Field: "x"}}))
	// Known field (exact and lowercase) is valid.
	assert.NoError(t, validateFilterFields[covFilterModel]([]Filter{{Field: "Email"}}))
	assert.NoError(t, validateFilterFields[covFilterModel]([]Filter{{Field: "id"}}))
	// Pointer element type is unwrapped to the struct.
	assert.NoError(t, validateFilterFields[*covFilterModel]([]Filter{{Field: "ID"}}))
	// Unknown field errors.
	err := validateFilterFields[covFilterModel]([]Filter{{Field: "bogus"}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownFilterField)
}

// --- getCheckpointWithRetry (projection_engine.go) ---

func TestGetCheckpointWithRetry(t *testing.T) {
	store := New(memory.NewAdapter())
	ctx := context.Background()

	t.Run("transient error recovers after retries", func(t *testing.T) {
		cp := newTestCheckpointStore()
		cp.getFailN = 2 // fail twice, then succeed (within checkpointReadRetries)
		e := NewProjectionEngine(store, WithCheckpointStore(cp))
		pos, err := e.getCheckpointWithRetry(ctx, "p")
		require.NoError(t, err)
		assert.Equal(t, uint64(0), pos)
	})

	t.Run("shutdown error returns immediately", func(t *testing.T) {
		cp := newTestCheckpointStore()
		cp.getErr = context.Canceled
		e := NewProjectionEngine(store, WithCheckpointStore(cp))
		_, err := e.getCheckpointWithRetry(ctx, "p")
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("persistent error is returned after exhausting retries", func(t *testing.T) {
		cp := newTestCheckpointStore()
		cp.getErr = assert.AnError
		e := NewProjectionEngine(store, WithCheckpointStore(cp))
		_, err := e.getCheckpointWithRetry(ctx, "p")
		assert.ErrorIs(t, err, assert.AnError)
	})

	t.Run("context cancellation during backoff returns ctx error", func(t *testing.T) {
		cp := newTestCheckpointStore()
		cp.getErr = assert.AnError // non-shutdown, so the loop reaches the backoff select
		e := NewProjectionEngine(store, WithCheckpointStore(cp))
		cctx, cancel := context.WithCancel(ctx)
		cancel() // already cancelled -> the backoff select takes ctx.Done immediately
		_, err := e.getCheckpointWithRetry(cctx, "p")
		assert.ErrorIs(t, err, context.Canceled)
	})
}

// --- UnregisteredEventTypes without an introspectable serializer (replay_safety.go) ---

// covNoIntrospectSerializer is a Serializer that does NOT implement EventTypeRegistrar.
type covNoIntrospectSerializer struct{}

func (covNoIntrospectSerializer) Serialize(interface{}) ([]byte, error) { return []byte("{}"), nil }
func (covNoIntrospectSerializer) Deserialize([]byte, string) (interface{}, error) {
	return map[string]interface{}{}, nil
}

func TestUnregisteredTypes_NoIntrospection(t *testing.T) {
	store := New(memory.NewAdapter(), WithSerializer(covNoIntrospectSerializer{}))
	ctx := context.Background()

	got, err := store.UnregisteredStreamTypes(ctx, "any")
	require.NoError(t, err)
	assert.Nil(t, got, "no introspection -> nil")

	all, err := store.UnregisteredEventTypes(ctx)
	require.NoError(t, err)
	assert.Nil(t, all, "no introspection -> nil")
}

package mink

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpcasterChain_Upcast_RecoversPanic(t *testing.T) {
	chain := NewUpcasterChain()
	require.NoError(t, chain.Register(newTestUpcaster("OrderCreated", 1, 2, func([]byte, Metadata) ([]byte, error) {
		panic("index out of range in user upcaster")
	})))
	require.NoError(t, chain.Register(newNoopUpcaster("OrderCreated", 2, 3)))

	payload := []byte(`{"secret":"SENSITIVE-PAYLOAD-VALUE"}`)

	var (
		out     []byte
		version int
		err     error
	)
	require.NotPanics(t, func() {
		out, version, err = chain.Upcast("OrderCreated", 1, payload, Metadata{})
	}, "a panicking upcaster must not unwind through Load/LoadAggregate")
	require.Error(t, err)
	assert.Nil(t, out)
	assert.Equal(t, 1, version, "the version reported is the one the failed transition started from")

	// Same error contract as an upcaster that returns an error.
	assert.ErrorIs(t, err, ErrUpcastFailed)
	var ue *UpcastError
	require.ErrorAs(t, err, &ue)
	assert.Equal(t, "OrderCreated", ue.EventType)
	assert.Equal(t, 1, ue.FromVersion)
	assert.Equal(t, 2, ue.ToVersion)

	// Descriptive (event type, versions, panic value) but never the payload.
	assert.Contains(t, err.Error(), "panicked")
	assert.Contains(t, err.Error(), "index out of range in user upcaster")
	assert.Contains(t, err.Error(), `"OrderCreated"`)
	assert.NotContains(t, err.Error(), "SENSITIVE-PAYLOAD-VALUE")
}

func TestUpcasterChain_Upcast_PanicAfterSuccessfulStep(t *testing.T) {
	chain := NewUpcasterChain()
	require.NoError(t, chain.Register(newTestUpcaster("OrderCreated", 1, 2, addJSONFieldUpcastFn("currency", "USD"))))
	require.NoError(t, chain.Register(newTestUpcaster("OrderCreated", 2, 3, func([]byte, Metadata) ([]byte, error) {
		panic("second step")
	})))

	out, version, err := chain.Upcast("OrderCreated", 1, []byte(`{"id":"o1"}`), Metadata{})
	require.Error(t, err)
	assert.Nil(t, out, "partially upcasted data is never returned alongside an error")
	assert.Equal(t, 2, version)
	var ue *UpcastError
	require.ErrorAs(t, err, &ue)
	assert.Equal(t, 2, ue.FromVersion)
	assert.Equal(t, 3, ue.ToVersion)
}

func TestUpcastingSerializer_DeserializeWithVersion_PanickingUpcaster(t *testing.T) {
	chain := NewUpcasterChain()
	require.NoError(t, chain.Register(newTestUpcaster("OrderCreated", 1, 2, func([]byte, Metadata) ([]byte, error) {
		panic("boom")
	})))
	inner := NewJSONSerializer()
	inner.Register("OrderCreated", OrderCreated{})
	s := NewUpcastingSerializer(inner, chain)

	var err error
	require.NotPanics(t, func() {
		_, err = s.DeserializeWithVersion([]byte(`{"id":"o1"}`), "OrderCreated", 1, Metadata{})
	})
	assert.ErrorIs(t, err, ErrUpcastFailed)
}

func TestSafeUpcast_PassesThrough(t *testing.T) {
	u := newTestUpcaster("OrderCreated", 1, 2, addJSONFieldUpcastFn("currency", "USD"))
	out, err := safeUpcast(u, []byte(`{"id":"o1"}`), Metadata{})
	require.NoError(t, err)
	assertJSONField(t, out, "currency", "USD")

	failing := newTestUpcaster("OrderCreated", 1, 2, func([]byte, Metadata) ([]byte, error) {
		return nil, assert.AnError
	})
	_, err = safeUpcast(failing, []byte(`{}`), Metadata{})
	assert.ErrorIs(t, err, assert.AnError, "an ordinary upcaster error is returned unchanged")
}

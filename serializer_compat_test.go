package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
)

// --- Fakes -------------------------------------------------------------------

// compatFakeAdapter satisfies adapters.EventStoreAdapter via an embedded (nil)
// interface — none of its methods are called by the compatibility check, which
// only type-asserts and reads RequiresJSONData.
type compatFakeAdapter struct {
	adapters.EventStoreAdapter
}

// compatJSONAdapter reports it requires JSON-encoded data (like the PostgreSQL
// JSONB adapter).
type compatJSONAdapter struct{ compatFakeAdapter }

func (compatJSONAdapter) RequiresJSONData() bool { return true }

// compatNonJSONAdapter implements JSONDataAdapter but opts out — it must be
// treated exactly like an adapter that does not implement the interface at all.
type compatNonJSONAdapter struct{ compatFakeAdapter }

func (compatNonJSONAdapter) RequiresJSONData() bool { return false }

// compatPlainAdapter does not implement adapters.JSONDataAdapter at all (like
// the in-memory adapter).
type compatPlainAdapter struct{ compatFakeAdapter }

// compatBinarySerializer is a serializer that declares a binary wire format.
type compatBinarySerializer struct{}

func (compatBinarySerializer) Serialize(interface{}) ([]byte, error)           { return []byte{0x80}, nil }
func (compatBinarySerializer) Deserialize([]byte, string) (interface{}, error) { return nil, nil }
func (compatBinarySerializer) BinaryFormat() bool                              { return true }

// compatPlainSerializer implements only the Serializer interface — no
// BinaryFormat method — and so must be treated as JSON-compatible (the historical
// default).
type compatPlainSerializer struct{}

func (compatPlainSerializer) Serialize(interface{}) ([]byte, error)           { return []byte("{}"), nil }
func (compatPlainSerializer) Deserialize([]byte, string) (interface{}, error) { return nil, nil }

// --- producesBinary ----------------------------------------------------------

func TestProducesBinary(t *testing.T) {
	tests := []struct {
		name string
		s    Serializer
		want bool
	}{
		{"json serializer is textual", NewJSONSerializer(), false},
		{"binary serializer", compatBinarySerializer{}, true},
		{"plain serializer defaults to textual", compatPlainSerializer{}, false},
		{"nil serializer", nil, false},
		{
			"upcasting decorator forwards binary inner",
			NewUpcastingSerializer(compatBinarySerializer{}, NewUpcasterChain()),
			true,
		},
		{
			"upcasting decorator forwards json inner",
			NewUpcastingSerializer(NewJSONSerializer(), NewUpcasterChain()),
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, producesBinary(tt.s))
		})
	}
}

// --- checkSerializerAdapterCompatible ----------------------------------------

func TestCheckSerializerAdapterCompatible(t *testing.T) {
	t.Run("json adapter rejects binary serializer", func(t *testing.T) {
		err := checkSerializerAdapterCompatible(compatBinarySerializer{}, compatJSONAdapter{})
		require.ErrorIs(t, err, ErrBinarySerializerUnsupported)
		assert.Contains(t, err.Error(), "BYTEA")
	})

	t.Run("json adapter accepts json serializer", func(t *testing.T) {
		assert.NoError(t, checkSerializerAdapterCompatible(NewJSONSerializer(), compatJSONAdapter{}))
	})

	t.Run("json adapter rejects binary serializer wrapped in upcaster", func(t *testing.T) {
		wrapped := NewUpcastingSerializer(compatBinarySerializer{}, NewUpcasterChain())
		err := checkSerializerAdapterCompatible(wrapped, compatJSONAdapter{})
		require.ErrorIs(t, err, ErrBinarySerializerUnsupported)
	})

	t.Run("adapter opting out of json accepts binary serializer", func(t *testing.T) {
		assert.NoError(t, checkSerializerAdapterCompatible(compatBinarySerializer{}, compatNonJSONAdapter{}))
	})

	t.Run("adapter without json requirement accepts binary serializer", func(t *testing.T) {
		assert.NoError(t, checkSerializerAdapterCompatible(compatBinarySerializer{}, compatPlainAdapter{}))
	})
}

// --- New integration ---------------------------------------------------------

func TestNew_BinarySerializerWithJSONAdapterDefersError(t *testing.T) {
	// New must NOT panic on an incompatible serializer/adapter pairing: a library constructor
	// must not panic on a recoverable configuration error (CLAUDE.md), and New has no error
	// return. It records the incompatibility and surfaces it as a clear typed error on first
	// write — never the cryptic driver error the underlying INSERT would otherwise raise.
	var store *EventStore
	require.NotPanics(t, func() {
		store = New(compatJSONAdapter{}, WithSerializer(compatBinarySerializer{}))
	})
	require.NotNil(t, store)

	// The recorded error surfaces on the first write, before the (nil) adapter is touched.
	err := store.Append(context.Background(), "stream-1", []interface{}{struct{}{}})
	require.ErrorIs(t, err, ErrBinarySerializerUnsupported)
}

func TestNew_JSONSerializerWithJSONAdapterOK(t *testing.T) {
	assert.NotPanics(t, func() {
		store := New(compatJSONAdapter{}) // default JSON serializer
		require.NotNil(t, store)
	})
}

func TestNew_BinarySerializerWithPlainAdapterOK(t *testing.T) {
	assert.NotPanics(t, func() {
		store := New(compatPlainAdapter{}, WithSerializer(compatBinarySerializer{}))
		require.NotNil(t, store)
	})
}

package mink

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventRegistry_Register_ConflictKeepsFirstRegistration(t *testing.T) {
	r := NewEventRegistry()
	r.Register("OrderCreated", OrderCreated{})
	assert.Nil(t, r.Conflicts())

	// A different Go type under an already-registered name is rejected, not overwritten.
	r.Register("OrderCreated", ItemAdded{})

	got, ok := r.Lookup("OrderCreated")
	require.True(t, ok)
	assert.Equal(t, reflect.TypeOf(OrderCreated{}), got, "the first registration wins")
	assert.Equal(t, 1, r.Count())
	assert.Equal(t, []string{"OrderCreated"}, r.Conflicts())

	// Deserialization keeps resolving to the first type.
	s := NewJSONSerializerWithRegistry(r)
	v, err := s.Deserialize([]byte(`{"id":"o1"}`), "OrderCreated")
	require.NoError(t, err)
	assert.IsType(t, OrderCreated{}, v)
}

func TestEventRegistry_Register_SameTypeIsIdempotent(t *testing.T) {
	r := NewEventRegistry()
	r.Register("OrderCreated", OrderCreated{})
	r.Register("OrderCreated", OrderCreated{})
	r.Register("OrderCreated", &OrderCreated{}) // pointer form of the same type

	assert.Equal(t, 1, r.Count())
	assert.Nil(t, r.Conflicts(), "re-registering the same type is not a conflict")
}

func TestEventRegistry_RegisterAll_ConflictOnStructName(t *testing.T) {
	r := NewEventRegistry()

	// Two DISTINCT Go types that share a struct name (as two packages' events would).
	first := func() interface{} {
		type OrderCreated struct{ ID string }
		return OrderCreated{}
	}()
	second := func() interface{} {
		type OrderCreated struct{ Total int }
		return OrderCreated{}
	}()
	require.NotEqual(t, reflect.TypeOf(first), reflect.TypeOf(second))
	require.Equal(t, reflect.TypeOf(first).Name(), reflect.TypeOf(second).Name())

	r.RegisterAll(first, ItemAdded{})
	r.RegisterAll(second, &ItemAdded{})

	got, ok := r.Lookup("OrderCreated")
	require.True(t, ok)
	assert.Equal(t, reflect.TypeOf(first), got)
	assert.Equal(t, 2, r.Count())
	assert.Equal(t, []string{"OrderCreated"}, r.Conflicts(), "ItemAdded re-registered as the same type is not a conflict")
}

func TestEventRegistry_Conflicts_SortedAndStable(t *testing.T) {
	r := NewEventRegistry()
	r.Register("Zulu", OrderCreated{})
	r.Register("Alpha", OrderCreated{})
	r.Register("Zulu", ItemAdded{})
	r.Register("Alpha", ItemAdded{})
	r.Register("Alpha", OrderShipped{}) // a second rejection of the same name is recorded once

	assert.Equal(t, []string{"Alpha", "Zulu"}, r.Conflicts())

	// Returned slices are independent copies.
	c := r.Conflicts()
	c[0] = "mutated"
	assert.Equal(t, []string{"Alpha", "Zulu"}, r.Conflicts())
}

func TestJSONSerializer_Register_ConflictVisibleViaRegistry(t *testing.T) {
	s := NewJSONSerializer()
	s.Register("OrderCreated", OrderCreated{})
	s.Register("OrderCreated", ItemAdded{})

	assert.True(t, s.IsRegistered("OrderCreated"))
	assert.Equal(t, []string{"OrderCreated"}, s.Registry().Conflicts())
}

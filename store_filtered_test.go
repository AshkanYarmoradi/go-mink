package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

func TestEventStore_LoadEventsFromPositionFiltered_Unsupported(t *testing.T) {
	// basicEventStoreAdapter implements only EventStoreAdapter, not FilteredFeedAdapter.
	store := New(&basicEventStoreAdapter{})

	_, err := store.LoadEventsFromPositionFiltered(context.Background(), 0, 100, adapters.FeedFilter{})
	assert.ErrorIs(t, err, ErrFilteredFeedNotSupported)
}

func TestEventStore_LoadEventsFromPositionFiltered_Memory(t *testing.T) {
	// Happy path through the store wrapper over the memory adapter (which does
	// implement FilteredFeedAdapter): the type-assert succeeds and adapter events are
	// converted to mink.StoredEvent.
	adapter := memory.NewAdapter()
	store := New(adapter)
	ctx := context.Background()

	_, err := adapter.Append(ctx, "order-1", []adapters.EventRecord{{Type: "OrderPlaced", Data: []byte(`{}`)}}, NoStream)
	require.NoError(t, err)
	_, err = adapter.Append(ctx, "order-1", []adapters.EventRecord{{Type: "OrderShipped", Data: []byte(`{}`)}}, AnyVersion)
	require.NoError(t, err)

	got, err := store.LoadEventsFromPositionFiltered(ctx, 0, 100, FeedFilter{EventTypes: []string{"OrderShipped"}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "OrderShipped", got[0].Type)
	assert.Equal(t, "order-1", got[0].StreamID)
}

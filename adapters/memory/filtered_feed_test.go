package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev"
	"go-mink.dev/adapters"
)

func TestMemoryAdapter_LoadFromPositionFiltered(t *testing.T) {
	ctx := context.Background()

	// Fixed feed: pos 1 order-1/OrderPlaced, 2 order-1/OrderShipped,
	// 3 order-2/OrderPlaced, 4 invoice-9/InvoiceCreated.
	setup := func(t *testing.T) *MemoryAdapter {
		t.Helper()
		a := NewAdapter()
		appendOne := func(stream, typ string, expected int64) {
			_, err := a.Append(ctx, stream, []adapters.EventRecord{{Type: typ, Data: []byte(`{}`)}}, expected)
			require.NoError(t, err)
		}
		appendOne("order-1", "OrderPlaced", mink.NoStream)
		appendOne("order-1", "OrderShipped", mink.AnyVersion)
		appendOne("order-2", "OrderPlaced", mink.NoStream)
		appendOne("invoice-9", "InvoiceCreated", mink.NoStream)
		return a
	}

	t.Run("event type filter returns only that type", func(t *testing.T) {
		got, err := setup(t).LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{EventTypes: []string{"OrderPlaced"}})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, uint64(1), got[0].GlobalPosition)
		assert.Equal(t, uint64(3), got[1].GlobalPosition)
	})

	t.Run("stream id set filter", func(t *testing.T) {
		got, err := setup(t).LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{StreamIDs: []string{"order-2", "invoice-9"}})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "order-2", got[0].StreamID)
		assert.Equal(t, "invoice-9", got[1].StreamID)
	})

	t.Run("category filter matches by stream prefix, not substring", func(t *testing.T) {
		got, err := setup(t).LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{Category: "order"})
		require.NoError(t, err)
		require.Len(t, got, 3) // order-1 x2 + order-2, not invoice-9
		assert.Equal(t, uint64(1), got[0].GlobalPosition)
		assert.Equal(t, uint64(2), got[1].GlobalPosition)
		assert.Equal(t, uint64(3), got[2].GlobalPosition)
	})

	t.Run("axes AND-compose", func(t *testing.T) {
		got, err := setup(t).LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{
			Category:   "order",
			EventTypes: []string{"OrderPlaced"},
		})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, uint64(1), got[0].GlobalPosition)
		assert.Equal(t, uint64(3), got[1].GlobalPosition)
	})

	t.Run("empty filter equals LoadFromPosition", func(t *testing.T) {
		a := setup(t)
		unfiltered, err := a.LoadFromPosition(ctx, 0, 100)
		require.NoError(t, err)
		filtered, err := a.LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{})
		require.NoError(t, err)
		assert.Equal(t, unfiltered, filtered)
	})

	t.Run("respects fromPosition and limit", func(t *testing.T) {
		// From position 1 (exclusive), OrderPlaced only ⇒ pos 3; limit 1.
		got, err := setup(t).LoadFromPositionFiltered(ctx, 1, 1, adapters.FeedFilter{EventTypes: []string{"OrderPlaced"}})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, uint64(3), got[0].GlobalPosition)
	})
}

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev"
	"go-mink.dev/adapters"
)

// TestBuildFeedFilterWhere covers the WHERE-fragment construction and LIKE escaping
// without a database, so the SQL-pushdown logic is verified in every CI run.
func TestBuildFeedFilterWhere(t *testing.T) {
	t.Run("empty filter contributes nothing", func(t *testing.T) {
		where, args, next := buildFeedFilterWhere(adapters.FeedFilter{}, 2)
		assert.Equal(t, "", where)
		assert.Empty(t, args)
		assert.Equal(t, 2, next)
	})

	t.Run("event types render an IN-list", func(t *testing.T) {
		where, args, next := buildFeedFilterWhere(adapters.FeedFilter{EventTypes: []string{"A", "B"}}, 2)
		assert.Equal(t, " AND event_type IN ($2,$3)", where)
		assert.Equal(t, []any{"A", "B"}, args)
		assert.Equal(t, 4, next)
	})

	t.Run("all axes keep placeholder order", func(t *testing.T) {
		where, args, next := buildFeedFilterWhere(adapters.FeedFilter{
			EventTypes: []string{"A"},
			StreamIDs:  []string{"s-1", "s-2"},
			Category:   "order_x",
		}, 2)
		assert.Equal(t, " AND event_type IN ($2) AND stream_id IN ($3,$4) AND stream_id LIKE $5", where)
		assert.Equal(t, []any{"A", "s-1", "s-2", `order\_x-%`}, args)
		assert.Equal(t, 6, next)
	})

	t.Run("category escapes LIKE metacharacters", func(t *testing.T) {
		_, args, _ := buildFeedFilterWhere(adapters.FeedFilter{Category: "a%b_c"}, 2)
		require.Len(t, args, 1)
		assert.Equal(t, `a\%b\_c-%`, args[0])
	})
}

func TestPostgresAdapter_LoadFromPositionFiltered(t *testing.T) {
	adapter := setupIntegrationTest(t) // skips without TEST_DATABASE_URL; isolated schema
	ctx := context.Background()

	// Fixed feed in an isolated schema: pos 1 order-1/OrderPlaced,
	// 2 order-1/OrderShipped, 3 order-2/OrderPlaced, 4 invoice-9/InvoiceCreated.
	appendOne := func(stream, typ string, expected int64) {
		_, err := adapter.Append(ctx, stream, []adapters.EventRecord{{Type: typ, Data: []byte(`{}`)}}, expected)
		require.NoError(t, err)
	}
	appendOne("order-1", "OrderPlaced", mink.NoStream)
	appendOne("order-1", "OrderShipped", mink.AnyVersion)
	appendOne("order-2", "OrderPlaced", mink.NoStream)
	appendOne("invoice-9", "InvoiceCreated", mink.NoStream)

	t.Run("event type filter", func(t *testing.T) {
		got, err := adapter.LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{EventTypes: []string{"OrderPlaced"}})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "OrderPlaced", got[0].Type)
		assert.Equal(t, "OrderPlaced", got[1].Type)
	})

	t.Run("stream id set", func(t *testing.T) {
		got, err := adapter.LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{StreamIDs: []string{"order-2", "invoice-9"}})
		require.NoError(t, err)
		require.Len(t, got, 2)
	})

	t.Run("category prefix", func(t *testing.T) {
		got, err := adapter.LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{Category: "order"})
		require.NoError(t, err)
		require.Len(t, got, 3)
	})

	t.Run("axes AND-compose", func(t *testing.T) {
		got, err := adapter.LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{Category: "order", EventTypes: []string{"OrderPlaced"}})
		require.NoError(t, err)
		require.Len(t, got, 2)
	})

	t.Run("empty filter equals LoadFromPosition", func(t *testing.T) {
		unfiltered, err := adapter.LoadFromPosition(ctx, 0, 100)
		require.NoError(t, err)
		filtered, err := adapter.LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{})
		require.NoError(t, err)
		require.Equal(t, len(unfiltered), len(filtered))
		for i := range unfiltered {
			assert.Equal(t, unfiltered[i].GlobalPosition, filtered[i].GlobalPosition)
		}
	})

	t.Run("fromPosition and limit", func(t *testing.T) {
		got, err := adapter.LoadFromPositionFiltered(ctx, 1, 1, adapters.FeedFilter{EventTypes: []string{"OrderPlaced"}})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, uint64(3), got[0].GlobalPosition)
	})

	t.Run("category with underscore does not over-match", func(t *testing.T) {
		// "order_9" must not LIKE-match the "order-*" streams once escaped.
		got, err := adapter.LoadFromPositionFiltered(ctx, 0, 100, adapters.FeedFilter{Category: "order_9"})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

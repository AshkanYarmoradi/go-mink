package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go-mink.dev"
	"go-mink.dev/adapters/memory"
)

func TestHomepageExample(t *testing.T) {
	ctx := context.Background()
	store := mink.New(memory.NewAdapter())
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	// Persisted events must deserialize back to the registered concrete types.
	require.NoError(t, appendOrder(ctx, store))
	summary, err := replayOrder(ctx, store)
	require.NoError(t, err)
	require.Equal(t, OrderSummary{CustomerID: "customer-456", Items: 2}, summary)

	// The displayed version guard must reject a duplicate creation.
	require.ErrorIs(t, appendOrder(ctx, store), mink.ErrConcurrencyConflict)
	events, err := store.Load(ctx, "order-123")
	require.NoError(t, err)
	require.Len(t, events, 2)
}

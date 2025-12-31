package mink

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventTypeFilter(t *testing.T) {
	filter := NewEventTypeFilter("OrderCreated", "OrderShipped")

	t.Run("matches included event types", func(t *testing.T) {
		event := StoredEvent{Type: "OrderCreated"}
		assert.True(t, filter.Matches(event))

		event2 := StoredEvent{Type: "OrderShipped"}
		assert.True(t, filter.Matches(event2))
	})

	t.Run("does not match excluded event types", func(t *testing.T) {
		event := StoredEvent{Type: "CustomerRegistered"}
		assert.False(t, filter.Matches(event))
	})
}

func TestCategoryFilter(t *testing.T) {
	filter := NewCategoryFilter("Order")

	t.Run("matches events from category", func(t *testing.T) {
		event := StoredEvent{StreamID: "Order-123"}
		assert.True(t, filter.Matches(event))
	})

	t.Run("does not match events from other categories", func(t *testing.T) {
		event := StoredEvent{StreamID: "Customer-456"}
		assert.False(t, filter.Matches(event))
	})

	t.Run("handles invalid stream ID", func(t *testing.T) {
		event := StoredEvent{StreamID: "invalid"}
		assert.False(t, filter.Matches(event))
	})
}

func TestCompositeFilter(t *testing.T) {
	typeFilter := NewEventTypeFilter("OrderCreated", "OrderShipped")
	categoryFilter := NewCategoryFilter("Order")
	composite := NewCompositeFilter(typeFilter, categoryFilter)

	t.Run("matches when all filters match", func(t *testing.T) {
		event := StoredEvent{
			Type:     "OrderCreated",
			StreamID: "Order-123",
		}
		assert.True(t, composite.Matches(event))
	})

	t.Run("does not match when any filter fails", func(t *testing.T) {
		// Wrong type
		event1 := StoredEvent{
			Type:     "CustomerRegistered",
			StreamID: "Order-123",
		}
		assert.False(t, composite.Matches(event1))

		// Wrong category
		event2 := StoredEvent{
			Type:     "OrderCreated",
			StreamID: "Customer-456",
		}
		assert.False(t, composite.Matches(event2))
	})

	t.Run("empty composite matches all", func(t *testing.T) {
		empty := NewCompositeFilter()
		event := StoredEvent{Type: "Any", StreamID: "Any-123"}
		assert.True(t, empty.Matches(event))
	})
}

func TestSubscriptionOptions(t *testing.T) {
	t.Run("default options", func(t *testing.T) {
		opts := DefaultSubscriptionOptions()

		assert.Equal(t, 256, opts.BufferSize)
		assert.True(t, opts.RetryOnError)
		assert.Equal(t, time.Second, opts.RetryInterval)
		assert.Equal(t, 5, opts.MaxRetries)
		assert.Nil(t, opts.Filter)
	})
}

func TestPollingSubscription(t *testing.T) {
	store := &EventStore{}

	t.Run("creates subscription", func(t *testing.T) {
		sub := NewPollingSubscription(store, 0)
		assert.NotNil(t, sub)
		assert.NotNil(t, sub.Events())
	})

	t.Run("creates subscription with options", func(t *testing.T) {
		opts := DefaultSubscriptionOptions()
		opts.BufferSize = 100
		sub := NewPollingSubscription(store, 0, opts)
		assert.NotNil(t, sub)
	})

	t.Run("close is idempotent", func(t *testing.T) {
		sub := NewPollingSubscription(store, 0)
		err := sub.Close()
		require.NoError(t, err)

		// Second close should also work
		err = sub.Close()
		require.NoError(t, err)
	})

	t.Run("Err returns nil initially", func(t *testing.T) {
		sub := NewPollingSubscription(store, 0)
		assert.Nil(t, sub.Err())
	})

	t.Run("setErr sets the error", func(t *testing.T) {
		sub := NewPollingSubscription(store, 0)
		expectedErr := assert.AnError
		sub.setErr(expectedErr)
		assert.Equal(t, expectedErr, sub.Err())
	})

	t.Run("Start and poll context cancellation", func(t *testing.T) {
		sub := NewPollingSubscription(store, 0)
		ctx, cancel := context.WithCancel(context.Background())

		// Start polling
		sub.Start(ctx, 10*time.Millisecond)

		// Wait a bit, then cancel
		time.Sleep(25 * time.Millisecond)
		cancel()

		// Wait for poll to stop
		time.Sleep(25 * time.Millisecond)

		// The error should be set to context.Canceled
		assert.ErrorIs(t, sub.Err(), context.Canceled)
	})

	t.Run("Start and poll stop via Close", func(t *testing.T) {
		sub := NewPollingSubscription(store, 0)
		ctx := context.Background()

		// Start polling
		sub.Start(ctx, 10*time.Millisecond)

		// Wait a bit, then close
		time.Sleep(25 * time.Millisecond)
		err := sub.Close()
		require.NoError(t, err)

		// Wait for poll to stop
		time.Sleep(25 * time.Millisecond)

		// No error expected for normal close
		assert.Nil(t, sub.Err())
	})
}

func TestCatchupSubscription(t *testing.T) {
	store := &EventStore{}

	t.Run("creates subscription", func(t *testing.T) {
		sub, err := NewCatchupSubscription(store, nil, 0)
		require.NoError(t, err)
		assert.NotNil(t, sub)
		assert.NotNil(t, sub.Events())
	})

	t.Run("creates subscription with options", func(t *testing.T) {
		opts := DefaultSubscriptionOptions()
		opts.BufferSize = 100
		sub, err := NewCatchupSubscription(store, nil, 0, opts)
		require.NoError(t, err)
		assert.NotNil(t, sub)
	})

	t.Run("close is idempotent", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, nil, 0)
		err := sub.Close()
		require.NoError(t, err)

		// Second close should also work
		err = sub.Close()
		require.NoError(t, err)
	})

	t.Run("Err returns nil initially", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, nil, 0)
		assert.Nil(t, sub.Err())
	})

	t.Run("setErr sets the error", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, nil, 0)
		expectedErr := assert.AnError
		sub.setErr(expectedErr)
		assert.Equal(t, expectedErr, sub.Err())
	})
}

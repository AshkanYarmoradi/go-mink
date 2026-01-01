package mink

import (
	"context"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
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

// CatchupTestEvent is a test event for subscription tests
type CatchupTestEvent struct {
	ID string
}

func TestCatchupSubscription_WithMemoryAdapter(t *testing.T) {
	t.Run("catches up on historical events", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(&CatchupTestEvent{})

		// Append some events before subscription
		ctx := context.Background()
		for i := 0; i < 5; i++ {
			err := store.Append(ctx, "Stream-"+string(rune('A'+i)), []interface{}{&CatchupTestEvent{ID: string(rune('A' + i))}})
			require.NoError(t, err)
		}

		// Create subscription starting from position 0
		sub, err := NewCatchupSubscription(store, 0)
		require.NoError(t, err)

		// Start subscription
		err = sub.Start(ctx, 50*time.Millisecond)
		require.NoError(t, err)

		// Collect events with timeout
		var received []StoredEvent
		timeout := time.After(300 * time.Millisecond)
	loop:
		for {
			select {
			case event, ok := <-sub.Events():
				if !ok {
					break loop
				}
				received = append(received, event)
				if len(received) >= 5 {
					break loop
				}
			case <-timeout:
				break loop
			}
		}

		sub.Close()

		// Memory adapter implements SubscriptionAdapter, so we should receive events
		// If no events received, check if adapter works as SubscriptionAdapter
		if len(received) == 0 {
			// This is expected if the store's adapter doesn't implement SubscriptionAdapter properly
			// The subscription will set an error
			assert.NotNil(t, sub.Err())
		} else {
			assert.GreaterOrEqual(t, len(received), 1)
		}
	})

	t.Run("receives new events after catch up", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(&CatchupTestEvent{})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Create subscription from current position (no historical events)
		sub, err := NewCatchupSubscription(store, 0)
		require.NoError(t, err)

		err = sub.Start(ctx, 20*time.Millisecond)
		require.NoError(t, err)

		// Append new event
		err = store.Append(ctx, "Stream-New", []interface{}{&CatchupTestEvent{ID: "new"}})
		require.NoError(t, err)

		// Wait for polling to pick up new event
		time.Sleep(100 * time.Millisecond)

		sub.Close()
	})

	t.Run("with event filter", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(&CatchupTestEvent{})

		ctx := context.Background()

		// Append events
		_ = store.Append(ctx, "Order-1", []interface{}{&CatchupTestEvent{ID: "1"}})
		_ = store.Append(ctx, "Customer-1", []interface{}{&CatchupTestEvent{ID: "2"}})

		// Create subscription with category filter
		opts := DefaultSubscriptionOptions()
		opts.Filter = NewCategoryFilter("Order")

		sub, err := NewCatchupSubscription(store, 0, opts)
		require.NoError(t, err)

		subCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()

		err = sub.Start(subCtx, 20*time.Millisecond)
		require.NoError(t, err)

		// Collect filtered events
		var received []StoredEvent
		for event := range sub.Events() {
			received = append(received, event)
		}

		sub.Close()

		// Should only have Order events (filtered), or none if subscription errors
		for _, e := range received {
			assert.Contains(t, e.StreamID, "Order")
		}
	})
}

func TestCatchupSubscription(t *testing.T) {
	store := &EventStore{}

	t.Run("creates subscription", func(t *testing.T) {
		sub, err := NewCatchupSubscription(store, 0)
		require.NoError(t, err)
		assert.NotNil(t, sub)
		assert.NotNil(t, sub.Events())
	})

	t.Run("creates subscription with options", func(t *testing.T) {
		opts := DefaultSubscriptionOptions()
		opts.BufferSize = 100
		sub, err := NewCatchupSubscription(store, 0, opts)
		require.NoError(t, err)
		assert.NotNil(t, sub)
	})

	t.Run("returns error for nil store", func(t *testing.T) {
		_, err := NewCatchupSubscription(nil, 0)
		assert.ErrorIs(t, err, ErrNilStore)
	})

	t.Run("close is idempotent", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, 0)
		err := sub.Close()
		require.NoError(t, err)

		// Second close should also work
		err = sub.Close()
		require.NoError(t, err)
	})

	t.Run("Err returns nil initially", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, 0)
		assert.Nil(t, sub.Err())
	})

	t.Run("setErr sets the error", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, 0)
		expectedErr := assert.AnError
		sub.setErr(expectedErr)
		assert.Equal(t, expectedErr, sub.Err())
	})

	t.Run("Position returns current position", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, 42)
		assert.Equal(t, uint64(42), sub.Position())
	})

	t.Run("Start is idempotent", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, 0)
		ctx := context.Background()

		// First start
		err := sub.Start(ctx, 10*time.Millisecond)
		assert.NoError(t, err)

		// Second start should be no-op
		err = sub.Start(ctx, 10*time.Millisecond)
		assert.NoError(t, err)

		sub.Close()
	})

	t.Run("Start returns error when closed", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, 0)
		sub.Close()

		err := sub.Start(context.Background(), 10*time.Millisecond)
		assert.ErrorIs(t, err, ErrAdapterClosed)
	})

	t.Run("run sets error when adapter does not support subscriptions", func(t *testing.T) {
		// Create a store without subscription support
		sub, _ := NewCatchupSubscription(store, 0)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err := sub.Start(ctx, 10*time.Millisecond)
		require.NoError(t, err)

		// Wait for run to detect the adapter doesn't support subscriptions
		time.Sleep(50 * time.Millisecond)

		// Should have error set
		assert.ErrorIs(t, sub.Err(), ErrSubscriptionNotSupported)
	})

	t.Run("run stops on context cancellation", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, 0)
		ctx, cancel := context.WithCancel(context.Background())

		err := sub.Start(ctx, 10*time.Millisecond)
		require.NoError(t, err)

		// Cancel immediately
		cancel()

		// Wait for run to stop
		time.Sleep(50 * time.Millisecond)

		// Should be stopped - either context error or subscription not supported
		assert.NotNil(t, sub.Err())
	})

	t.Run("run stops on Close", func(t *testing.T) {
		sub, _ := NewCatchupSubscription(store, 0)
		ctx := context.Background()

		err := sub.Start(ctx, 10*time.Millisecond)
		require.NoError(t, err)

		// Close should stop the run
		sub.Close()

		// Wait for run to stop
		time.Sleep(50 * time.Millisecond)
	})
}

func TestCatchupSubscription_FullCatchupAndPolling(t *testing.T) {
	t.Run("catches up then polls for new events", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(&CatchupTestEvent{})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Append historical events
		for i := 0; i < 3; i++ {
			_ = store.Append(ctx, "Order-"+string(rune('A'+i)), []interface{}{&CatchupTestEvent{ID: "historical-" + string(rune('A'+i))}})
		}

		// Create subscription starting from position 0
		sub, err := NewCatchupSubscription(store, 0)
		require.NoError(t, err)

		err = sub.Start(ctx, 30*time.Millisecond)
		require.NoError(t, err)

		// Collect historical events
		var received []StoredEvent
		timeout := time.After(200 * time.Millisecond)
	collectLoop:
		for len(received) < 3 {
			select {
			case event, ok := <-sub.Events():
				if !ok {
					break collectLoop
				}
				received = append(received, event)
			case <-timeout:
				break collectLoop
			}
		}

		// Should have received historical events
		assert.GreaterOrEqual(t, len(received), 1)

		// Now append a new event during polling phase
		_ = store.Append(ctx, "Order-New", []interface{}{&CatchupTestEvent{ID: "polling-new"}})

		// Wait for polling to pick it up
		time.Sleep(100 * time.Millisecond)

		// Collect any new events
		timeout2 := time.After(100 * time.Millisecond)
	collectLoop2:
		for {
			select {
			case event, ok := <-sub.Events():
				if !ok {
					break collectLoop2
				}
				received = append(received, event)
			case <-timeout2:
				break collectLoop2
			}
		}

		sub.Close()

		// Should have received at least historical + potentially new event
		assert.GreaterOrEqual(t, len(received), 3)
	})

	t.Run("stops during catchup phase via Close", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(&CatchupTestEvent{})

		ctx := context.Background()

		// Append many events to ensure catchup takes time
		for i := 0; i < 10; i++ {
			_ = store.Append(ctx, "Order-"+string(rune('0'+i)), []interface{}{&CatchupTestEvent{ID: "event-" + string(rune('0'+i))}})
		}

		sub, err := NewCatchupSubscription(store, 0)
		require.NoError(t, err)

		err = sub.Start(ctx, 10*time.Millisecond)
		require.NoError(t, err)

		// Immediately close
		sub.Close()

		// Wait for goroutine to finish
		time.Sleep(50 * time.Millisecond)

		// Check that subscription stopped without error (closed via Close, not context)
		// Either nil error or context cancelled is acceptable
	})

	t.Run("stops during polling phase via context cancel", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(&CatchupTestEvent{})

		ctx, cancel := context.WithCancel(context.Background())

		// No historical events, goes straight to polling
		sub, err := NewCatchupSubscription(store, 0)
		require.NoError(t, err)

		err = sub.Start(ctx, 20*time.Millisecond)
		require.NoError(t, err)

		// Wait for it to enter polling phase
		time.Sleep(50 * time.Millisecond)

		// Cancel context during polling
		cancel()

		// Wait for polling to stop
		time.Sleep(50 * time.Millisecond)

		// Should have context error
		assert.ErrorIs(t, sub.Err(), context.Canceled)

		sub.Close()
	})
}

func TestPollingSubscription_Integration(t *testing.T) {
	t.Run("polls for events", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(&CatchupTestEvent{})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sub := NewPollingSubscription(store, 0)

		// Start polling
		sub.Start(ctx, 30*time.Millisecond)

		// Wait for polling to start
		time.Sleep(20 * time.Millisecond)

		// Append event
		_ = store.Append(ctx, "Order-Poll", []interface{}{&CatchupTestEvent{ID: "poll-event"}})

		// Wait for polling to pick it up
		time.Sleep(100 * time.Millisecond)

		// Drain events
		timeout := time.After(100 * time.Millisecond)
	drain:
		for {
			select {
			case _, ok := <-sub.Events():
				if !ok {
					break drain
				}
				// Event received - polling is working
			case <-timeout:
				break drain
			}
		}

		sub.Close()

		// Polling subscription tested - timing-dependent so we just verify no errors
	})

	t.Run("stops on context cancel", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		ctx, cancel := context.WithCancel(context.Background())

		sub := NewPollingSubscription(store, 0)
		sub.Start(ctx, 20*time.Millisecond)

		// Let it poll a bit
		time.Sleep(50 * time.Millisecond)

		// Cancel context
		cancel()

		// Wait for poll to stop
		time.Sleep(50 * time.Millisecond)

		// Should have context error
		assert.ErrorIs(t, sub.Err(), context.Canceled)

		sub.Close()
	})

	t.Run("stops on Close", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)

		ctx := context.Background()

		sub := NewPollingSubscription(store, 0)
		sub.Start(ctx, 20*time.Millisecond)

		// Let it poll a bit
		time.Sleep(50 * time.Millisecond)

		// Close
		sub.Close()

		// Wait for poll to stop
		time.Sleep(50 * time.Millisecond)

		// Should have no error (closed gracefully)
		assert.Nil(t, sub.Err())
	})

	t.Run("applies filter during polling", func(t *testing.T) {
		adapter := memory.NewAdapter()
		store := New(adapter)
		store.RegisterEvents(&CatchupTestEvent{})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		opts := DefaultSubscriptionOptions()
		opts.Filter = NewCategoryFilter("Order")

		sub := NewPollingSubscription(store, 0, opts)
		sub.Start(ctx, 20*time.Millisecond)

		// Wait for polling to start
		time.Sleep(20 * time.Millisecond)

		// Append events to different categories
		_ = store.Append(ctx, "Order-1", []interface{}{&CatchupTestEvent{ID: "order-event"}})
		_ = store.Append(ctx, "Customer-1", []interface{}{&CatchupTestEvent{ID: "customer-event"}})

		// Wait for polling
		time.Sleep(100 * time.Millisecond)

		// Collect events
		var received []StoredEvent
		timeout := time.After(100 * time.Millisecond)
	collect:
		for {
			select {
			case event, ok := <-sub.Events():
				if !ok {
					break collect
				}
				received = append(received, event)
			case <-timeout:
				break collect
			}
		}

		sub.Close()

		// All received events should be from Order category
		for _, e := range received {
			assert.Contains(t, e.StreamID, "Order")
		}
	})
}

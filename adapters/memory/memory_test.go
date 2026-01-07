package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAdapter(t *testing.T) {
	t.Run("creates adapter with defaults", func(t *testing.T) {
		adapter := NewAdapter()

		assert.NotNil(t, adapter)
		assert.Equal(t, 0, adapter.EventCount())
		assert.Equal(t, 0, adapter.StreamCount())
	})
}

func TestMemoryAdapter_Initialize(t *testing.T) {
	t.Run("Initialize is no-op", func(t *testing.T) {
		adapter := NewAdapter()

		err := adapter.Initialize(context.Background())

		assert.NoError(t, err)
	})
}

func TestMemoryAdapter_Append(t *testing.T) {
	t.Run("append to new stream", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		events := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{"orderId":"123"}`)},
		}

		stored, err := adapter.Append(ctx, "Order-123", events, mink.NoStream)

		require.NoError(t, err)
		require.Len(t, stored, 1)
		assert.Equal(t, "Order-123", stored[0].StreamID)
		assert.Equal(t, "OrderCreated", stored[0].Type)
		assert.Equal(t, int64(1), stored[0].Version)
		assert.Equal(t, uint64(1), stored[0].GlobalPosition)
		assert.NotEmpty(t, stored[0].ID)
	})

	t.Run("append multiple events", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		events := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
		}

		stored, err := adapter.Append(ctx, "Order-123", events, mink.NoStream)

		require.NoError(t, err)
		require.Len(t, stored, 3)
		assert.Equal(t, int64(1), stored[0].Version)
		assert.Equal(t, int64(2), stored[1].Version)
		assert.Equal(t, int64(3), stored[2].Version)
	})

	t.Run("append to existing stream", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Create stream
		events1 := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "Order-123", events1, mink.NoStream)
		require.NoError(t, err)

		// Append more events
		events2 := []adapters.EventRecord{{Type: "ItemAdded", Data: []byte(`{}`)}}
		stored, err := adapter.Append(ctx, "Order-123", events2, 1)

		require.NoError(t, err)
		assert.Equal(t, int64(2), stored[0].Version)
	})

	t.Run("concurrency conflict on wrong version", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Create stream with version 1
		events1 := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "Order-123", events1, mink.NoStream)
		require.NoError(t, err)

		// Try to append with wrong expected version
		events2 := []adapters.EventRecord{{Type: "ItemAdded", Data: []byte(`{}`)}}
		_, err = adapter.Append(ctx, "Order-123", events2, 0) // Expected 0, actual 1

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrConcurrencyConflict))

		var concErr *ConcurrencyError
		require.True(t, errors.As(err, &concErr))
		assert.Equal(t, "Order-123", concErr.StreamID)
		assert.Equal(t, int64(0), concErr.ExpectedVersion)
		assert.Equal(t, int64(1), concErr.ActualVersion)
	})

	t.Run("concurrency conflict when stream exists with NoStream", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Create stream
		events1 := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "Order-123", events1, mink.NoStream)
		require.NoError(t, err)

		// Try to create again
		_, err = adapter.Append(ctx, "Order-123", events1, mink.NoStream)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrConcurrencyConflict))
	})

	t.Run("AnyVersion skips check", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}

		// Create with AnyVersion
		_, err := adapter.Append(ctx, "Order-123", events, mink.AnyVersion)
		require.NoError(t, err)

		// Append with AnyVersion
		_, err = adapter.Append(ctx, "Order-123", events, mink.AnyVersion)
		require.NoError(t, err)

		assert.Equal(t, 2, adapter.EventCount())
	})

	t.Run("StreamExists requires existing stream", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}

		// Try to append to non-existent stream
		_, err := adapter.Append(ctx, "Order-123", events, mink.StreamExists)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrStreamNotFound))
	})

	t.Run("empty stream ID", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}

		_, err := adapter.Append(ctx, "", events, mink.NoStream)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrEmptyStreamID))
	})

	t.Run("no events", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		_, err := adapter.Append(ctx, "Order-123", []adapters.EventRecord{}, mink.NoStream)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrNoEvents))
	})

	t.Run("invalid version", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}

		_, err := adapter.Append(ctx, "Order-123", events, -5) // Invalid negative version

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrInvalidVersion))
	})

	t.Run("preserves metadata", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		metadata := adapters.Metadata{
			CorrelationID: "corr-123",
			CausationID:   "cause-456",
			UserID:        "user-789",
			TenantID:      "tenant-abc",
			Custom:        map[string]string{"key": "value"},
		}

		events := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`), Metadata: metadata},
		}

		stored, err := adapter.Append(ctx, "Order-123", events, mink.NoStream)

		require.NoError(t, err)
		assert.Equal(t, metadata.CorrelationID, stored[0].Metadata.CorrelationID)
		assert.Equal(t, metadata.CausationID, stored[0].Metadata.CausationID)
		assert.Equal(t, metadata.UserID, stored[0].Metadata.UserID)
		assert.Equal(t, metadata.TenantID, stored[0].Metadata.TenantID)
		assert.Equal(t, "value", stored[0].Metadata.Custom["key"])
	})

	t.Run("context cancellation", func(t *testing.T) {
		adapter := NewAdapter()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}

		_, err := adapter.Append(ctx, "Order-123", events, mink.NoStream)

		assert.Error(t, err)
	})

	t.Run("closed adapter", func(t *testing.T) {
		adapter := NewAdapter()
		adapter.Close()

		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}

		_, err := adapter.Append(context.Background(), "Order-123", events, mink.NoStream)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))
	})
}

func TestMemoryAdapter_Load(t *testing.T) {
	t.Run("load empty stream returns empty slice", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		events, err := adapter.Load(ctx, "Order-123", 0)

		require.NoError(t, err)
		assert.Empty(t, events)
	})

	t.Run("load all events", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Create events
		records := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
		}
		_, err := adapter.Append(ctx, "Order-123", records, mink.NoStream)
		require.NoError(t, err)

		events, err := adapter.Load(ctx, "Order-123", 0)

		require.NoError(t, err)
		assert.Len(t, events, 2)
		assert.Equal(t, "OrderCreated", events[0].Type)
		assert.Equal(t, "ItemAdded", events[1].Type)
	})

	t.Run("load from version", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Create events
		records := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
		}
		_, err := adapter.Append(ctx, "Order-123", records, mink.NoStream)
		require.NoError(t, err)

		events, err := adapter.Load(ctx, "Order-123", 1)

		require.NoError(t, err)
		assert.Len(t, events, 2)
		assert.Equal(t, int64(2), events[0].Version)
		assert.Equal(t, int64(3), events[1].Version)
	})

	t.Run("empty stream ID", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		_, err := adapter.Load(ctx, "", 0)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrEmptyStreamID))
	})

	t.Run("context cancellation", func(t *testing.T) {
		adapter := NewAdapter()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := adapter.Load(ctx, "Order-123", 0)

		assert.Error(t, err)
	})
}

func TestMemoryAdapter_GetStreamInfo(t *testing.T) {
	t.Run("stream not found", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		_, err := adapter.GetStreamInfo(ctx, "Order-123")

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrStreamNotFound))
	})

	t.Run("returns stream info", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		events := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
		}
		_, err := adapter.Append(ctx, "Order-123", events, mink.NoStream)
		require.NoError(t, err)

		info, err := adapter.GetStreamInfo(ctx, "Order-123")

		require.NoError(t, err)
		assert.Equal(t, "Order-123", info.StreamID)
		assert.Equal(t, "Order", info.Category)
		assert.Equal(t, int64(2), info.Version)
		assert.False(t, info.CreatedAt.IsZero())
		assert.False(t, info.UpdatedAt.IsZero())
	})
}

func TestMemoryAdapter_GetLastPosition(t *testing.T) {
	t.Run("returns 0 for empty store", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		pos, err := adapter.GetLastPosition(ctx)

		require.NoError(t, err)
		assert.Equal(t, uint64(0), pos)
	})

	t.Run("returns global position", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Add events to multiple streams
		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, _ = adapter.Append(ctx, "Order-1", events, mink.NoStream)
		_, _ = adapter.Append(ctx, "Order-2", events, mink.NoStream)
		_, _ = adapter.Append(ctx, "Order-3", events, mink.NoStream)

		pos, err := adapter.GetLastPosition(ctx)

		require.NoError(t, err)
		assert.Equal(t, uint64(3), pos)
	})
}

func TestMemoryAdapter_Close(t *testing.T) {
	t.Run("close releases resources", func(t *testing.T) {
		adapter := NewAdapter()

		err := adapter.Close()

		assert.NoError(t, err)
	})

	t.Run("operations fail after close", func(t *testing.T) {
		adapter := NewAdapter()
		adapter.Close()
		ctx := context.Background()

		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}

		_, err := adapter.Append(ctx, "Order-123", events, mink.NoStream)
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		_, err = adapter.Load(ctx, "Order-123", 0)
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		_, err = adapter.GetStreamInfo(ctx, "Order-123")
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))
	})
}

func TestMemoryAdapter_Snapshots(t *testing.T) {
	t.Run("save and load snapshot", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		data := []byte(`{"state":"test"}`)
		err := adapter.SaveSnapshot(ctx, "Order-123", 5, data)
		require.NoError(t, err)

		snapshot, err := adapter.LoadSnapshot(ctx, "Order-123")

		require.NoError(t, err)
		require.NotNil(t, snapshot)
		assert.Equal(t, "Order-123", snapshot.StreamID)
		assert.Equal(t, int64(5), snapshot.Version)
		assert.Equal(t, data, snapshot.Data)
	})

	t.Run("load non-existent snapshot", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		snapshot, err := adapter.LoadSnapshot(ctx, "Order-123")

		require.NoError(t, err)
		assert.Nil(t, snapshot)
	})

	t.Run("delete snapshot", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Save snapshot
		err := adapter.SaveSnapshot(ctx, "Order-123", 5, []byte(`{}`))
		require.NoError(t, err)

		// Delete snapshot
		err = adapter.DeleteSnapshot(ctx, "Order-123")
		require.NoError(t, err)

		// Verify deleted
		snapshot, err := adapter.LoadSnapshot(ctx, "Order-123")
		require.NoError(t, err)
		assert.Nil(t, snapshot)
	})
}

func TestMemoryAdapter_Checkpoints(t *testing.T) {
	t.Run("get and set checkpoint", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		err := adapter.SetCheckpoint(ctx, "OrderProjection", 100)
		require.NoError(t, err)

		pos, err := adapter.GetCheckpoint(ctx, "OrderProjection")

		require.NoError(t, err)
		assert.Equal(t, uint64(100), pos)
	})

	t.Run("get non-existent checkpoint returns 0", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		pos, err := adapter.GetCheckpoint(ctx, "OrderProjection")

		require.NoError(t, err)
		assert.Equal(t, uint64(0), pos)
	})
}

func TestMemoryAdapter_Ping(t *testing.T) {
	t.Run("ping healthy adapter", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		err := adapter.Ping(ctx)

		assert.NoError(t, err)
	})

	t.Run("ping closed adapter", func(t *testing.T) {
		adapter := NewAdapter()
		adapter.Close()

		err := adapter.Ping(context.Background())

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))
	})
}

func TestMemoryAdapter_Reset(t *testing.T) {
	t.Run("reset clears all data", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Add some data
		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, _ = adapter.Append(ctx, "Order-123", events, mink.NoStream)
		_ = adapter.SaveSnapshot(ctx, "Order-123", 1, []byte(`{}`))
		_ = adapter.SetCheckpoint(ctx, "Projection", 100)

		// Reset
		adapter.Reset()

		assert.Equal(t, 0, adapter.EventCount())
		assert.Equal(t, 0, adapter.StreamCount())

		pos, _ := adapter.GetLastPosition(ctx)
		assert.Equal(t, uint64(0), pos)
	})
}

func TestMemoryAdapter_Concurrent(t *testing.T) {
	t.Run("concurrent appends to different streams", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		var wg sync.WaitGroup
		numStreams := 100

		for i := 0; i < numStreams; i++ {
			wg.Add(1)
			go func(streamNum int) {
				defer wg.Done()

				streamID := "Order-" + string(rune('A'+streamNum%26)) + "-" + string(rune('0'+streamNum%10))
				events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}

				_, err := adapter.Append(ctx, streamID, events, mink.AnyVersion)
				assert.NoError(t, err)
			}(i)
		}

		wg.Wait()
		assert.Equal(t, numStreams, adapter.EventCount())
	})

	t.Run("concurrent reads and writes", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Pre-populate
		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, _ = adapter.Append(ctx, "Order-123", events, mink.NoStream)

		var wg sync.WaitGroup

		// Writers
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				events := []adapters.EventRecord{{Type: "ItemAdded", Data: []byte(`{}`)}}
				_, _ = adapter.Append(ctx, "Order-123", events, mink.AnyVersion)
			}()
		}

		// Readers
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = adapter.Load(ctx, "Order-123", 0)
			}()
		}

		wg.Wait()
	})
}

func TestMemoryAdapter_Subscriptions(t *testing.T) {
	t.Run("subscribe to all events", func(t *testing.T) {
		adapter := NewAdapter()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Subscribe
		ch, err := adapter.SubscribeAll(ctx, 0)
		require.NoError(t, err)

		// Append events
		go func() {
			time.Sleep(50 * time.Millisecond)
			events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
			_, _ = adapter.Append(context.Background(), "Order-123", events, mink.NoStream)
		}()

		// Receive event
		select {
		case event := <-ch:
			assert.Equal(t, "OrderCreated", event.Type)
		case <-ctx.Done():
			t.Fatal("timeout waiting for event")
		}
	})

	t.Run("receive historical events", func(t *testing.T) {
		adapter := NewAdapter()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Add events first
		events := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
		}
		_, _ = adapter.Append(context.Background(), "Order-123", events, mink.NoStream)

		// Subscribe
		ch, err := adapter.SubscribeAll(ctx, 0)
		require.NoError(t, err)

		// Receive historical events
		received := make([]adapters.StoredEvent, 0)
		for i := 0; i < 2; i++ {
			select {
			case event := <-ch:
				received = append(received, event)
			case <-ctx.Done():
				t.Fatal("timeout waiting for event")
			}
		}

		assert.Len(t, received, 2)
	})
}

func TestExtractCategory(t *testing.T) {
	tests := []struct {
		streamID string
		expected string
	}{
		{"Order-123", "Order"},
		{"Customer-abc-def", "Customer"},
		{"NoHyphen", "NoHyphen"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.streamID, func(t *testing.T) {
			result := adapters.ExtractCategory(tt.streamID)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMemoryAdapter_GetStreamInfo_ContextCancellation(t *testing.T) {
	adapter := NewAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := adapter.GetStreamInfo(ctx, "Order-123")
	assert.Error(t, err)
}

func TestMemoryAdapter_GetLastPosition_ContextCancellation(t *testing.T) {
	adapter := NewAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := adapter.GetLastPosition(ctx)
	assert.Error(t, err)
}

func TestMemoryAdapter_Snapshots_ContextCancellation(t *testing.T) {
	adapter := NewAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := adapter.SaveSnapshot(ctx, "Order-123", 1, []byte(`{}`))
	assert.Error(t, err)

	_, err = adapter.LoadSnapshot(ctx, "Order-123")
	assert.Error(t, err)

	err = adapter.DeleteSnapshot(ctx, "Order-123")
	assert.Error(t, err)
}

func TestMemoryAdapter_Checkpoints_ContextCancellation(t *testing.T) {
	adapter := NewAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := adapter.SetCheckpoint(ctx, "Projection", 100)
	assert.Error(t, err)

	_, err = adapter.GetCheckpoint(ctx, "Projection")
	assert.Error(t, err)
}

func TestMemoryAdapter_Ping_ContextCancellation(t *testing.T) {
	adapter := NewAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := adapter.Ping(ctx)
	assert.Error(t, err)
}

func TestMemoryAdapter_SubscribeStream(t *testing.T) {
	t.Run("filters events by stream", func(t *testing.T) {
		adapter := NewAdapter()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Add events to multiple streams
		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, _ = adapter.Append(context.Background(), "Order-1", events, mink.NoStream)
		_, _ = adapter.Append(context.Background(), "Order-2", events, mink.NoStream)
		_, _ = adapter.Append(context.Background(), "Order-1", []adapters.EventRecord{{Type: "ItemAdded", Data: []byte(`{}`)}}, 1)

		// Subscribe to Order-1 only
		ch, err := adapter.SubscribeStream(ctx, "Order-1", 0)
		require.NoError(t, err)

		// Should receive 2 events from Order-1
		received := make([]adapters.StoredEvent, 0)
		timeout := time.After(500 * time.Millisecond)
		for {
			select {
			case event := <-ch:
				received = append(received, event)
				if len(received) >= 2 {
					goto done
				}
			case <-timeout:
				goto done
			}
		}
	done:
		assert.Len(t, received, 2)
		for _, e := range received {
			assert.Equal(t, "Order-1", e.StreamID)
		}
	})
}

func TestMemoryAdapter_SubscribeCategory(t *testing.T) {
	t.Run("filters events by category", func(t *testing.T) {
		adapter := NewAdapter()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Add events to multiple categories
		events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, _ = adapter.Append(context.Background(), "Order-1", events, mink.NoStream)
		_, _ = adapter.Append(context.Background(), "Customer-1", events, mink.NoStream)
		_, _ = adapter.Append(context.Background(), "Order-2", events, mink.NoStream)

		// Subscribe to Order category only
		ch, err := adapter.SubscribeCategory(ctx, "Order", 0)
		require.NoError(t, err)

		// Should receive 2 events from Order category
		received := make([]adapters.StoredEvent, 0)
		timeout := time.After(500 * time.Millisecond)
		for {
			select {
			case event := <-ch:
				received = append(received, event)
				if len(received) >= 2 {
					goto done
				}
			case <-timeout:
				goto done
			}
		}
	done:
		assert.Len(t, received, 2)
	})
}

func TestMemoryAdapter_SubscribeStream_ContextCancellation(t *testing.T) {
	adapter := NewAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := adapter.SubscribeStream(ctx, "Order-1", 0)
	assert.Error(t, err)
}

func TestMemoryAdapter_SubscribeCategory_ContextCancellation(t *testing.T) {
	adapter := NewAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := adapter.SubscribeCategory(ctx, "Order", 0)
	assert.Error(t, err)
}

func TestMemoryAdapter_LoadFromPosition(t *testing.T) {
	t.Run("returns events from position", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Create some events
		for i := 0; i < 5; i++ {
			_, err := adapter.Append(ctx, "Order-1", []adapters.EventRecord{
				{Type: "OrderCreated", Data: []byte(`{}`)},
			}, mink.AnyVersion)
			require.NoError(t, err)
		}

		// Load from position 2
		events, err := adapter.LoadFromPosition(ctx, 2, 10)
		require.NoError(t, err)
		assert.Len(t, events, 3) // Events 3, 4, 5
		assert.Equal(t, uint64(3), events[0].GlobalPosition)
	})

	t.Run("returns empty when no events after position", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		_, err := adapter.Append(ctx, "Order-1", []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
		}, mink.NoStream)
		require.NoError(t, err)

		events, err := adapter.LoadFromPosition(ctx, 10, 10)
		require.NoError(t, err)
		assert.Empty(t, events)
	})

	t.Run("respects limit", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Create 10 events
		for i := 0; i < 10; i++ {
			_, err := adapter.Append(ctx, "Order-1", []adapters.EventRecord{
				{Type: "OrderCreated", Data: []byte(`{}`)},
			}, mink.AnyVersion)
			require.NoError(t, err)
		}

		// Load with limit of 3
		events, err := adapter.LoadFromPosition(ctx, 0, 3)
		require.NoError(t, err)
		assert.Len(t, events, 3)
	})

	t.Run("uses default limit when 0 or negative", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		_, err := adapter.Append(ctx, "Order-1", []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
		}, mink.NoStream)
		require.NoError(t, err)

		events, err := adapter.LoadFromPosition(ctx, 0, 0)
		require.NoError(t, err)
		assert.Len(t, events, 1)

		events, err = adapter.LoadFromPosition(ctx, 0, -1)
		require.NoError(t, err)
		assert.Len(t, events, 1)
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		adapter := NewAdapter()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := adapter.LoadFromPosition(ctx, 0, 10)
		assert.Error(t, err)
	})
}

func TestConcurrencyError(t *testing.T) {
	t.Run("Error message contains details", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", 5, 7)

		assert.Contains(t, err.Error(), "Order-123")
		assert.Contains(t, err.Error(), "5")
		assert.Contains(t, err.Error(), "7")
	})

	t.Run("Is adapters.ErrConcurrencyConflict", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", 5, 7)
		assert.True(t, errors.Is(err, adapters.ErrConcurrencyConflict))
	})

	t.Run("errors.As extracts details", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", 5, 7)

		var concErr *ConcurrencyError
		require.True(t, errors.As(err, &concErr))
		assert.Equal(t, "Order-123", concErr.StreamID)
		assert.Equal(t, int64(5), concErr.ExpectedVersion)
		assert.Equal(t, int64(7), concErr.ActualVersion)
	})
}

func TestStreamNotFoundError(t *testing.T) {
	t.Run("Error message contains stream ID", func(t *testing.T) {
		err := NewStreamNotFoundError("Order-123")

		assert.Contains(t, err.Error(), "Order-123")
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("Is adapters.ErrStreamNotFound", func(t *testing.T) {
		err := NewStreamNotFoundError("Order-123")
		assert.True(t, errors.Is(err, adapters.ErrStreamNotFound))
	})

	t.Run("errors.As extracts details", func(t *testing.T) {
		err := NewStreamNotFoundError("Order-123")

		var snfErr *StreamNotFoundError
		require.True(t, errors.As(err, &snfErr))
		assert.Equal(t, "Order-123", snfErr.StreamID)
	})
}

// Benchmarks for memory adapter
func BenchmarkMemoryAdapter_Append(b *testing.B) {
	adapter := NewAdapter()
	ctx := context.Background()
	events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{"orderId":"123"}`)}}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		streamID := "Order-" + string(rune(i))
		_, _ = adapter.Append(ctx, streamID, events, mink.AnyVersion)
	}
}

func BenchmarkMemoryAdapter_Load(b *testing.B) {
	adapter := NewAdapter()
	ctx := context.Background()

	// Setup: create stream with events
	events := []adapters.EventRecord{
		{Type: "OrderCreated", Data: []byte(`{}`)},
		{Type: "ItemAdded", Data: []byte(`{}`)},
		{Type: "ItemAdded", Data: []byte(`{}`)},
	}
	_, _ = adapter.Append(ctx, "Order-bench", events, mink.NoStream)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = adapter.Load(ctx, "Order-bench", 0)
	}
}

func BenchmarkMemoryAdapter_Concurrent(b *testing.B) {
	adapter := NewAdapter()
	ctx := context.Background()
	events := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			streamID := "Order-" + string(rune(i%1000))
			_, _ = adapter.Append(ctx, streamID, events, mink.AnyVersion)
			i++
		}
	})
}

// ============================================================================
// Consolidated Closed Adapter Tests
// ============================================================================

// TestMemoryAdapter_ClosedAdapterErrors consolidates all "returns error on closed adapter"
// tests into a single table-driven test. This reduces code duplication while maintaining
// the same test coverage.
func TestMemoryAdapter_ClosedAdapterErrors(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		fn   func(*MemoryAdapter) error
	}{
		// Core operations
		{"Load", func(a *MemoryAdapter) error { _, err := a.Load(ctx, "Order-123", 0); return err }},
		{"GetStreamInfo", func(a *MemoryAdapter) error { _, err := a.GetStreamInfo(ctx, "Order-123"); return err }},
		{"GetLastPosition", func(a *MemoryAdapter) error { _, err := a.GetLastPosition(ctx); return err }},
		{"LoadFromPosition", func(a *MemoryAdapter) error { _, err := a.LoadFromPosition(ctx, 0, 10); return err }},
		// Snapshots
		{"SaveSnapshot", func(a *MemoryAdapter) error { return a.SaveSnapshot(ctx, "Order-123", 1, []byte(`{}`)) }},
		{"LoadSnapshot", func(a *MemoryAdapter) error { _, err := a.LoadSnapshot(ctx, "Order-123"); return err }},
		{"DeleteSnapshot", func(a *MemoryAdapter) error { return a.DeleteSnapshot(ctx, "Order-123") }},
		// Checkpoints
		{"SetCheckpoint", func(a *MemoryAdapter) error { return a.SetCheckpoint(ctx, "Projection", 100) }},
		{"GetCheckpoint", func(a *MemoryAdapter) error { _, err := a.GetCheckpoint(ctx, "Projection"); return err }},
		// Subscriptions
		{"SubscribeAll", func(a *MemoryAdapter) error { _, err := a.SubscribeAll(ctx, 0); return err }},
		// Migrations
		{"GetAppliedMigrations", func(a *MemoryAdapter) error { _, err := a.GetAppliedMigrations(ctx); return err }},
		{"RecordMigration", func(a *MemoryAdapter) error { return a.RecordMigration(ctx, "test") }},
		{"RemoveMigrationRecord", func(a *MemoryAdapter) error { return a.RemoveMigrationRecord(ctx, "test") }},
		{"ExecuteSQL", func(a *MemoryAdapter) error { return a.ExecuteSQL(ctx, "SELECT 1") }},
		// Diagnostics
		{"GetDiagnosticInfo", func(a *MemoryAdapter) error { _, err := a.GetDiagnosticInfo(ctx); return err }},
		{"CheckSchema", func(a *MemoryAdapter) error { _, err := a.CheckSchema(ctx, "mink_events"); return err }},
		{"GetProjectionHealth", func(a *MemoryAdapter) error { _, err := a.GetProjectionHealth(ctx); return err }},
		// Projections
		{"ListProjections", func(a *MemoryAdapter) error { _, err := a.ListProjections(ctx); return err }},
		{"GetProjection", func(a *MemoryAdapter) error { _, err := a.GetProjection(ctx, "test"); return err }},
		{"SetProjectionStatus", func(a *MemoryAdapter) error { return a.SetProjectionStatus(ctx, "test", "paused") }},
		{"ResetProjectionCheckpoint", func(a *MemoryAdapter) error { return a.ResetProjectionCheckpoint(ctx, "test") }},
		{"GetTotalEventCount", func(a *MemoryAdapter) error { _, err := a.GetTotalEventCount(ctx); return err }},
		{"GetEventStoreStats", func(a *MemoryAdapter) error { _, err := a.GetEventStoreStats(ctx); return err }},
		// Streams
		{"ListStreams", func(a *MemoryAdapter) error { _, err := a.ListStreams(ctx, "", 10); return err }},
		{"GetStreamEvents", func(a *MemoryAdapter) error { _, err := a.GetStreamEvents(ctx, "stream-1", 0, 10); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewAdapter()
			_ = adapter.Close()

			err := tt.fn(adapter)
			assert.ErrorIs(t, err, adapters.ErrAdapterClosed)
		})
	}
}

// ============================================================================
// Migration Tests
// ============================================================================

func TestMemoryAdapter_GetAppliedMigrations(t *testing.T) {
	// Clean up migrations between tests
	memoryMigrationsMu.Lock()
	memoryMigrations = make(map[string]*migrationRecord)
	memoryMigrationsMu.Unlock()

	t.Run("returns empty when no migrations", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		migrations, err := adapter.GetAppliedMigrations(ctx)

		require.NoError(t, err)
		assert.Empty(t, migrations)
	})

	t.Run("returns sorted migration names", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Record migrations out of order
		_ = adapter.RecordMigration(ctx, "003_migration")
		_ = adapter.RecordMigration(ctx, "001_migration")
		_ = adapter.RecordMigration(ctx, "002_migration")

		migrations, err := adapter.GetAppliedMigrations(ctx)

		require.NoError(t, err)
		assert.Len(t, migrations, 3)
		assert.Equal(t, []string{"001_migration", "002_migration", "003_migration"}, migrations)

		// Clean up
		memoryMigrationsMu.Lock()
		memoryMigrations = make(map[string]*migrationRecord)
		memoryMigrationsMu.Unlock()
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		adapter := NewAdapter()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := adapter.GetAppliedMigrations(ctx)

		assert.Error(t, err)
	})
}

func TestMemoryAdapter_RecordMigration(t *testing.T) {
	// Clean up migrations between tests
	memoryMigrationsMu.Lock()
	memoryMigrations = make(map[string]*migrationRecord)
	memoryMigrationsMu.Unlock()

	t.Run("records migration successfully", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		err := adapter.RecordMigration(ctx, "001_initial")

		require.NoError(t, err)

		// Verify it was recorded
		migrations, err := adapter.GetAppliedMigrations(ctx)
		require.NoError(t, err)
		assert.Contains(t, migrations, "001_initial")

		// Clean up
		memoryMigrationsMu.Lock()
		memoryMigrations = make(map[string]*migrationRecord)
		memoryMigrationsMu.Unlock()
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		adapter := NewAdapter()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := adapter.RecordMigration(ctx, "001_initial")

		assert.Error(t, err)
	})
}

func TestMemoryAdapter_RemoveMigrationRecord(t *testing.T) {
	// Clean up migrations between tests
	memoryMigrationsMu.Lock()
	memoryMigrations = make(map[string]*migrationRecord)
	memoryMigrationsMu.Unlock()

	t.Run("removes existing migration", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Record then remove
		_ = adapter.RecordMigration(ctx, "001_initial")
		err := adapter.RemoveMigrationRecord(ctx, "001_initial")

		require.NoError(t, err)

		// Verify it was removed
		migrations, err := adapter.GetAppliedMigrations(ctx)
		require.NoError(t, err)
		assert.NotContains(t, migrations, "001_initial")
	})

	t.Run("no error when removing non-existent migration", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		err := adapter.RemoveMigrationRecord(ctx, "nonexistent")

		assert.NoError(t, err)
	})
}

func TestMemoryAdapter_ExecuteSQL(t *testing.T) {
	t.Run("executes SQL successfully (no-op)", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		err := adapter.ExecuteSQL(ctx, "CREATE TABLE test (id INT)")

		assert.NoError(t, err)
	})
}

func TestMemoryAdapter_GenerateSchema(t *testing.T) {
	t.Run("returns schema with project name", func(t *testing.T) {
		adapter := NewAdapter()

		schema := adapter.GenerateSchema("test-project", "mink_events", "mink_snapshots", "mink_outbox")

		assert.Contains(t, schema, "test-project")
		assert.Contains(t, schema, "In-Memory")
		assert.Contains(t, schema, "memory adapter")
	})
}

// ============================================================================
// Diagnostic Tests
// ============================================================================

func TestMemoryAdapter_GetDiagnosticInfo(t *testing.T) {
	t.Run("returns diagnostic info successfully", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		info, err := adapter.GetDiagnosticInfo(ctx)

		require.NoError(t, err)
		assert.True(t, info.Connected)
		assert.Contains(t, info.Version, "In-Memory")
		assert.Contains(t, info.Message, "in-memory")
	})
}

func TestMemoryAdapter_CheckSchema(t *testing.T) {
	t.Run("returns schema check with no events", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		result, err := adapter.CheckSchema(ctx, "mink_events")

		require.NoError(t, err)
		assert.True(t, result.TableExists)
		assert.Equal(t, int64(0), result.EventCount)
		assert.Contains(t, result.Message, "active")
	})

	t.Run("returns schema check with events", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Add some events
		_, _ = adapter.Append(ctx, "Order-1", []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
		}, mink.NoStream)

		result, err := adapter.CheckSchema(ctx, "mink_events")

		require.NoError(t, err)
		assert.True(t, result.TableExists)
		assert.Equal(t, int64(2), result.EventCount)
		assert.Contains(t, result.Message, "2 events")
	})
}

func TestMemoryAdapter_GetProjectionHealth(t *testing.T) {
	// Clean up projections between tests
	memoryProjectionsMu.Lock()
	memoryProjections = make(map[string]*projectionInfo)
	memoryProjectionsMu.Unlock()

	t.Run("returns health with no projections", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		result, err := adapter.GetProjectionHealth(ctx)

		require.NoError(t, err)
		assert.Equal(t, int64(0), result.TotalProjections)
		assert.Contains(t, result.Message, "No projections")
	})

	t.Run("returns health with all projections up to date", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Register a projection that's up to date
		memoryProjectionsMu.Lock()
		memoryProjections["test-projection"] = &projectionInfo{
			name:      "test-projection",
			position:  0,
			status:    "running",
			updatedAt: time.Now(),
		}
		memoryProjectionsMu.Unlock()

		result, err := adapter.GetProjectionHealth(ctx)

		require.NoError(t, err)
		assert.Equal(t, int64(1), result.TotalProjections)
		assert.Equal(t, int64(0), result.ProjectionsBehind)
		assert.Contains(t, result.Message, "all up to date")

		// Clean up
		memoryProjectionsMu.Lock()
		memoryProjections = make(map[string]*projectionInfo)
		memoryProjectionsMu.Unlock()
	})

	t.Run("returns health with projections behind", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Add events to increase global position
		_, _ = adapter.Append(ctx, "Order-1", []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
		}, mink.NoStream)

		// Register a projection that's behind
		memoryProjectionsMu.Lock()
		memoryProjections["test-projection"] = &projectionInfo{
			name:      "test-projection",
			position:  0,
			status:    "running",
			updatedAt: time.Now(),
		}
		memoryProjectionsMu.Unlock()

		result, err := adapter.GetProjectionHealth(ctx)

		require.NoError(t, err)
		assert.Equal(t, int64(1), result.TotalProjections)
		assert.Equal(t, int64(1), result.ProjectionsBehind)
		assert.Contains(t, result.Message, "behind")

		// Clean up
		memoryProjectionsMu.Lock()
		memoryProjections = make(map[string]*projectionInfo)
		memoryProjectionsMu.Unlock()
	})
}

// ============================================================================
// Projection Management Tests
// ============================================================================

func TestMemoryAdapter_ListProjections(t *testing.T) {
	// Clean up projections between tests
	memoryProjectionsMu.Lock()
	memoryProjections = make(map[string]*projectionInfo)
	memoryProjectionsMu.Unlock()

	t.Run("returns empty when no projections", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		projections, err := adapter.ListProjections(ctx)

		require.NoError(t, err)
		assert.Empty(t, projections)
	})

	t.Run("returns all projections", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Register projections
		memoryProjectionsMu.Lock()
		memoryProjections["projection-1"] = &projectionInfo{
			name:      "projection-1",
			position:  10,
			status:    "running",
			updatedAt: time.Now(),
		}
		memoryProjections["projection-2"] = &projectionInfo{
			name:      "projection-2",
			position:  5,
			status:    "stopped",
			updatedAt: time.Now(),
		}
		memoryProjectionsMu.Unlock()

		projections, err := adapter.ListProjections(ctx)

		require.NoError(t, err)
		assert.Len(t, projections, 2)

		// Clean up
		memoryProjectionsMu.Lock()
		memoryProjections = make(map[string]*projectionInfo)
		memoryProjectionsMu.Unlock()
	})
}

func TestMemoryAdapter_GetProjection(t *testing.T) {
	// Clean up projections between tests
	memoryProjectionsMu.Lock()
	memoryProjections = make(map[string]*projectionInfo)
	memoryProjectionsMu.Unlock()

	t.Run("returns nil for non-existent projection", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		projection, err := adapter.GetProjection(ctx, "nonexistent")

		require.NoError(t, err)
		assert.Nil(t, projection)
	})

	t.Run("returns projection info", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Register projection
		memoryProjectionsMu.Lock()
		memoryProjections["test-projection"] = &projectionInfo{
			name:      "test-projection",
			position:  42,
			status:    "running",
			updatedAt: time.Now(),
		}
		memoryProjectionsMu.Unlock()

		projection, err := adapter.GetProjection(ctx, "test-projection")

		require.NoError(t, err)
		require.NotNil(t, projection)
		assert.Equal(t, "test-projection", projection.Name)
		assert.Equal(t, int64(42), projection.Position)
		assert.Equal(t, "running", projection.Status)

		// Clean up
		memoryProjectionsMu.Lock()
		memoryProjections = make(map[string]*projectionInfo)
		memoryProjectionsMu.Unlock()
	})
}

func TestMemoryAdapter_SetProjectionStatus(t *testing.T) {
	// Clean up projections between tests
	memoryProjectionsMu.Lock()
	memoryProjections = make(map[string]*projectionInfo)
	memoryProjectionsMu.Unlock()

	t.Run("sets status for existing projection", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Register projection
		memoryProjectionsMu.Lock()
		memoryProjections["test-projection"] = &projectionInfo{
			name:      "test-projection",
			position:  0,
			status:    "running",
			updatedAt: time.Now(),
		}
		memoryProjectionsMu.Unlock()

		err := adapter.SetProjectionStatus(ctx, "test-projection", "stopped")

		require.NoError(t, err)

		// Verify status changed
		projection, _ := adapter.GetProjection(ctx, "test-projection")
		assert.Equal(t, "stopped", projection.Status)

		// Clean up
		memoryProjectionsMu.Lock()
		memoryProjections = make(map[string]*projectionInfo)
		memoryProjectionsMu.Unlock()
	})

	t.Run("returns error for non-existent projection", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		err := adapter.SetProjectionStatus(ctx, "nonexistent", "stopped")

		assert.ErrorIs(t, err, adapters.ErrStreamNotFound)
	})
}

func TestMemoryAdapter_ResetProjectionCheckpoint(t *testing.T) {
	// Clean up projections between tests
	memoryProjectionsMu.Lock()
	memoryProjections = make(map[string]*projectionInfo)
	memoryProjectionsMu.Unlock()

	t.Run("resets checkpoint for projection", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Set initial checkpoint
		_ = adapter.SetCheckpoint(ctx, "test-projection", 100)

		// Register projection
		memoryProjectionsMu.Lock()
		memoryProjections["test-projection"] = &projectionInfo{
			name:      "test-projection",
			position:  100,
			status:    "running",
			updatedAt: time.Now(),
		}
		memoryProjectionsMu.Unlock()

		err := adapter.ResetProjectionCheckpoint(ctx, "test-projection")

		require.NoError(t, err)

		// Verify checkpoint reset
		checkpoint, _ := adapter.GetCheckpoint(ctx, "test-projection")
		assert.Equal(t, uint64(0), checkpoint)

		// Clean up
		memoryProjectionsMu.Lock()
		memoryProjections = make(map[string]*projectionInfo)
		memoryProjectionsMu.Unlock()
	})
}

func TestMemoryAdapter_GetTotalEventCount(t *testing.T) {
	t.Run("returns 0 when no events", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		count, err := adapter.GetTotalEventCount(ctx)

		require.NoError(t, err)
		assert.Equal(t, int64(0), count)
	})

	t.Run("returns correct count after appending", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Add events
		_, _ = adapter.Append(ctx, "Order-1", []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
		}, mink.NoStream)

		_, _ = adapter.Append(ctx, "Order-2", []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
		}, mink.NoStream)

		count, err := adapter.GetTotalEventCount(ctx)

		require.NoError(t, err)
		assert.Equal(t, int64(3), count)
	})
}

// ============================================================================
// Statistics Tests
// ============================================================================

func TestMemoryAdapter_GetEventStoreStats(t *testing.T) {
	t.Run("returns empty stats for new adapter", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		stats, err := adapter.GetEventStoreStats(ctx)

		require.NoError(t, err)
		assert.Equal(t, int64(0), stats.TotalEvents)
		assert.Equal(t, int64(0), stats.TotalStreams)
		assert.Empty(t, stats.TopEventTypes)
	})

	t.Run("returns stats with events", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Add events
		_, _ = adapter.Append(ctx, "Order-1", []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
		}, mink.NoStream)

		stats, err := adapter.GetEventStoreStats(ctx)

		require.NoError(t, err)
		assert.Equal(t, int64(3), stats.TotalEvents)
		assert.Equal(t, int64(1), stats.TotalStreams)
		assert.Len(t, stats.TopEventTypes, 2)
	})

	t.Run("returns top 5 event types only", func(t *testing.T) {
		adapter := NewAdapter()
		ctx := context.Background()

		// Add events with 7 different types
		for i := 0; i < 7; i++ {
			_, _ = adapter.Append(ctx, "Stream-1", []adapters.EventRecord{
				{Type: "EventType" + string(rune('A'+i)), Data: []byte(`{}`)},
			}, mink.AnyVersion)
		}

		stats, err := adapter.GetEventStoreStats(ctx)

		require.NoError(t, err)
		assert.Len(t, stats.TopEventTypes, 5)
	})
}

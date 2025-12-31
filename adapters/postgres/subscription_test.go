package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresSubscription_LoadFromPosition(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	adapter, err := NewAdapter(connStr, WithSchema("mink_sub_test"))
	require.NoError(t, err)
	defer adapter.Close()

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	// Clean up
	adapter.db.ExecContext(ctx, "TRUNCATE TABLE mink_sub_test.events CASCADE")

	// Append some events
	events := []adapters.EventRecord{
		{Type: "TestEvent1", Data: []byte(`{"seq": 1}`)},
		{Type: "TestEvent2", Data: []byte(`{"seq": 2}`)},
		{Type: "TestEvent3", Data: []byte(`{"seq": 3}`)},
	}
	_, err = adapter.Append(ctx, "Test-001", events, NoStream)
	require.NoError(t, err)

	t.Run("loads events from position 0", func(t *testing.T) {
		loaded, err := adapter.LoadFromPosition(ctx, 0, 100)
		require.NoError(t, err)
		assert.Len(t, loaded, 3)
	})

	t.Run("loads events from specific position", func(t *testing.T) {
		loaded, err := adapter.LoadFromPosition(ctx, 1, 100)
		require.NoError(t, err)
		assert.Len(t, loaded, 2)
	})

	t.Run("respects limit", func(t *testing.T) {
		loaded, err := adapter.LoadFromPosition(ctx, 0, 2)
		require.NoError(t, err)
		assert.Len(t, loaded, 2)
	})

	t.Run("returns empty for position past end", func(t *testing.T) {
		loaded, err := adapter.LoadFromPosition(ctx, 1000, 100)
		require.NoError(t, err)
		assert.Empty(t, loaded)
	})

	t.Run("fails when closed", func(t *testing.T) {
		closedAdapter, _ := NewAdapter(connStr)
		closedAdapter.Close()

		_, err := closedAdapter.LoadFromPosition(ctx, 0, 100)
		assert.ErrorIs(t, err, ErrAdapterClosed)
	})
}

func TestPostgresSubscription_SubscribeAll(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	adapter, err := NewAdapter(connStr, WithSchema("mink_sub_all_test"))
	require.NoError(t, err)
	defer adapter.Close()

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	// Clean up
	adapter.db.ExecContext(ctx, "TRUNCATE TABLE mink_sub_all_test.events CASCADE")

	// Append some events
	events := []adapters.EventRecord{
		{Type: "TestEvent1", Data: []byte(`{}`)},
		{Type: "TestEvent2", Data: []byte(`{}`)},
	}
	_, err = adapter.Append(ctx, "Test-001", events, NoStream)
	require.NoError(t, err)

	t.Run("subscribes and receives historical events", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		ch, err := adapter.SubscribeAll(ctx, 0)
		require.NoError(t, err)

		var received []adapters.StoredEvent
		for event := range ch {
			received = append(received, event)
		}

		assert.Len(t, received, 2)
	})

	t.Run("fails when closed", func(t *testing.T) {
		closedAdapter, _ := NewAdapter(connStr)
		closedAdapter.Close()

		_, err := closedAdapter.SubscribeAll(context.Background(), 0)
		assert.ErrorIs(t, err, ErrAdapterClosed)
	})
}

func TestPostgresSubscription_SubscribeStream(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	adapter, err := NewAdapter(connStr, WithSchema("mink_sub_stream_test"))
	require.NoError(t, err)
	defer adapter.Close()

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	// Clean up
	adapter.db.ExecContext(ctx, "TRUNCATE TABLE mink_sub_stream_test.events CASCADE")

	// Append events to multiple streams
	events1 := []adapters.EventRecord{{Type: "Event1", Data: []byte(`{}`)}}
	events2 := []adapters.EventRecord{{Type: "Event2", Data: []byte(`{}`)}}
	adapter.Append(ctx, "Stream-001", events1, NoStream)
	adapter.Append(ctx, "Stream-002", events2, NoStream)

	t.Run("subscribes to specific stream", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		ch, err := adapter.SubscribeStream(ctx, "Stream-001", 0)
		require.NoError(t, err)

		var received []adapters.StoredEvent
		for event := range ch {
			received = append(received, event)
		}

		assert.Len(t, received, 1)
		assert.Equal(t, "Event1", received[0].Type)
	})
}

func TestPostgresSubscription_SubscribeCategory(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	adapter, err := NewAdapter(connStr, WithSchema("mink_sub_cat_test"))
	require.NoError(t, err)
	defer adapter.Close()

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	// Clean up
	adapter.db.ExecContext(ctx, "TRUNCATE TABLE mink_sub_cat_test.events CASCADE")

	// Append events to different categories
	adapter.Append(ctx, "Order-001", []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}, NoStream)
	adapter.Append(ctx, "Order-002", []adapters.EventRecord{{Type: "OrderShipped", Data: []byte(`{}`)}}, NoStream)
	adapter.Append(ctx, "Customer-001", []adapters.EventRecord{{Type: "CustomerRegistered", Data: []byte(`{}`)}}, NoStream)

	t.Run("subscribes to category", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		ch, err := adapter.SubscribeCategory(ctx, "Order", 0)
		require.NoError(t, err)

		var received []adapters.StoredEvent
		for event := range ch {
			received = append(received, event)
		}

		assert.Len(t, received, 2)
	})
}

func TestParseMetadata(t *testing.T) {
	t.Run("parses metadata with correlation ID", func(t *testing.T) {
		data := []byte(`{"correlationId":"corr-123","causationId":"cause-456"}`)
		var m adapters.Metadata
		err := parseMetadata(data, &m)
		require.NoError(t, err)
		assert.Equal(t, "corr-123", m.CorrelationID)
		assert.Equal(t, "cause-456", m.CausationID)
	})

	t.Run("handles empty metadata", func(t *testing.T) {
		var m adapters.Metadata
		err := parseMetadata([]byte{}, &m)
		require.NoError(t, err)
	})

	t.Run("handles empty object", func(t *testing.T) {
		var m adapters.Metadata
		err := parseMetadata([]byte(`{}`), &m)
		require.NoError(t, err)
	})

	t.Run("parses custom fields", func(t *testing.T) {
		data := []byte(`{"userId":"user-1","tenantId":"tenant-1","custom":{"key":"value"}}`)
		var m adapters.Metadata
		err := parseMetadata(data, &m)
		require.NoError(t, err)
		assert.Equal(t, "user-1", m.UserID)
		assert.Equal(t, "tenant-1", m.TenantID)
		assert.Equal(t, "value", m.Custom["key"])
	})
}

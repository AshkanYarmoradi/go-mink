package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getTestDB returns a database connection for testing.
// Set TEST_DATABASE_URL environment variable to run integration tests.
func getTestDB(t *testing.T) *sql.DB {
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}

	db, err := sql.Open("pgx", connStr)
	require.NoError(t, err)

	return db
}

// cleanupSchema drops the test schema.
func cleanupSchema(t *testing.T, db *sql.DB, schema string) {
	_, err := db.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
	require.NoError(t, err)
}

func TestPostgresAdapter_Initialize(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	defer cleanupSchema(t, db, schema)

	adapter := NewAdapterWithDB(db, WithSchema(schema))

	t.Run("creates schema and tables", func(t *testing.T) {
		err := adapter.Initialize(context.Background())
		require.NoError(t, err)

		// Verify tables exist
		var exists bool
		err = db.QueryRow(`
			SELECT EXISTS (
				SELECT FROM information_schema.tables 
				WHERE table_schema = $1 AND table_name = 'events'
			)`, schema).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("idempotent initialization", func(t *testing.T) {
		err := adapter.Initialize(context.Background())
		require.NoError(t, err)

		err = adapter.Initialize(context.Background())
		require.NoError(t, err)
	})
}

func TestPostgresAdapter_Append(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	defer cleanupSchema(t, db, schema)

	adapter := NewAdapterWithDB(db, WithSchema(schema))
	require.NoError(t, adapter.Initialize(context.Background()))

	ctx := context.Background()

	t.Run("append to new stream", func(t *testing.T) {
		events := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{"orderId":"123"}`)},
		}

		stored, err := adapter.Append(ctx, "Order-123", events, mink.NoStream)

		require.NoError(t, err)
		require.Len(t, stored, 1)
		assert.Equal(t, "Order-123", stored[0].StreamID)
		assert.Equal(t, "OrderCreated", stored[0].Type)
		assert.Equal(t, int64(1), stored[0].Version)
		assert.NotZero(t, stored[0].GlobalPosition)
		assert.NotEmpty(t, stored[0].ID)
		assert.False(t, stored[0].Timestamp.IsZero())
	})

	t.Run("append multiple events", func(t *testing.T) {
		events := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
		}

		stored, err := adapter.Append(ctx, "Order-456", events, mink.NoStream)

		require.NoError(t, err)
		require.Len(t, stored, 3)
		assert.Equal(t, int64(1), stored[0].Version)
		assert.Equal(t, int64(2), stored[1].Version)
		assert.Equal(t, int64(3), stored[2].Version)

		// Global positions should be sequential
		assert.True(t, stored[1].GlobalPosition > stored[0].GlobalPosition)
		assert.True(t, stored[2].GlobalPosition > stored[1].GlobalPosition)
	})

	t.Run("append to existing stream", func(t *testing.T) {
		// Create stream
		events1 := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "Order-789", events1, mink.NoStream)
		require.NoError(t, err)

		// Append more events
		events2 := []adapters.EventRecord{{Type: "ItemAdded", Data: []byte(`{}`)}}
		stored, err := adapter.Append(ctx, "Order-789", events2, 1)

		require.NoError(t, err)
		assert.Equal(t, int64(2), stored[0].Version)
	})

	t.Run("concurrency conflict", func(t *testing.T) {
		// Create stream
		events1 := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "Order-conflict", events1, mink.NoStream)
		require.NoError(t, err)

		// Try to append with wrong version
		events2 := []adapters.EventRecord{{Type: "ItemAdded", Data: []byte(`{}`)}}
		_, err = adapter.Append(ctx, "Order-conflict", events2, 0)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, mink.ErrConcurrencyConflict))
	})

	t.Run("preserves metadata", func(t *testing.T) {
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

		stored, err := adapter.Append(ctx, "Order-meta", events, mink.NoStream)

		require.NoError(t, err)
		assert.Equal(t, metadata.CorrelationID, stored[0].Metadata.CorrelationID)
		assert.Equal(t, metadata.UserID, stored[0].Metadata.UserID)
		assert.Equal(t, "value", stored[0].Metadata.Custom["key"])
	})

	t.Run("empty stream ID", func(t *testing.T) {
		events := []adapters.EventRecord{{Type: "Test", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "", events, mink.NoStream)
		assert.True(t, errors.Is(err, mink.ErrEmptyStreamID))
	})

	t.Run("no events", func(t *testing.T) {
		_, err := adapter.Append(ctx, "Order-empty", []adapters.EventRecord{}, mink.NoStream)
		assert.True(t, errors.Is(err, mink.ErrNoEvents))
	})
}

func TestPostgresAdapter_Load(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	defer cleanupSchema(t, db, schema)

	adapter := NewAdapterWithDB(db, WithSchema(schema))
	require.NoError(t, adapter.Initialize(context.Background()))

	ctx := context.Background()

	t.Run("load empty stream", func(t *testing.T) {
		events, err := adapter.Load(ctx, "Order-nonexistent", 0)

		require.NoError(t, err)
		assert.Empty(t, events)
	})

	t.Run("load all events", func(t *testing.T) {
		// Create events
		records := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{"id":"1"}`)},
			{Type: "ItemAdded", Data: []byte(`{"id":"2"}`)},
		}
		_, err := adapter.Append(ctx, "Order-load-all", records, mink.NoStream)
		require.NoError(t, err)

		events, err := adapter.Load(ctx, "Order-load-all", 0)

		require.NoError(t, err)
		assert.Len(t, events, 2)
		assert.Equal(t, "OrderCreated", events[0].Type)
		assert.Equal(t, "ItemAdded", events[1].Type)
	})

	t.Run("load from version", func(t *testing.T) {
		// Create events
		records := []adapters.EventRecord{
			{Type: "E1", Data: []byte(`{}`)},
			{Type: "E2", Data: []byte(`{}`)},
			{Type: "E3", Data: []byte(`{}`)},
		}
		_, err := adapter.Append(ctx, "Order-load-from", records, mink.NoStream)
		require.NoError(t, err)

		events, err := adapter.Load(ctx, "Order-load-from", 1)

		require.NoError(t, err)
		assert.Len(t, events, 2)
		assert.Equal(t, int64(2), events[0].Version)
		assert.Equal(t, int64(3), events[1].Version)
	})
}

func TestPostgresAdapter_GetStreamInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	defer cleanupSchema(t, db, schema)

	adapter := NewAdapterWithDB(db, WithSchema(schema))
	require.NoError(t, adapter.Initialize(context.Background()))

	ctx := context.Background()

	t.Run("stream not found", func(t *testing.T) {
		_, err := adapter.GetStreamInfo(ctx, "Order-notfound")

		assert.Error(t, err)
		assert.True(t, errors.Is(err, mink.ErrStreamNotFound))
	})

	t.Run("returns stream info", func(t *testing.T) {
		events := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte(`{}`)},
			{Type: "ItemAdded", Data: []byte(`{}`)},
		}
		_, err := adapter.Append(ctx, "Order-info", events, mink.NoStream)
		require.NoError(t, err)

		info, err := adapter.GetStreamInfo(ctx, "Order-info")

		require.NoError(t, err)
		assert.Equal(t, "Order-info", info.StreamID)
		assert.Equal(t, "Order", info.Category)
		assert.Equal(t, int64(2), info.Version)
	})
}

func TestPostgresAdapter_GetLastPosition(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	defer cleanupSchema(t, db, schema)

	adapter := NewAdapterWithDB(db, WithSchema(schema))
	require.NoError(t, adapter.Initialize(context.Background()))

	ctx := context.Background()

	t.Run("empty store returns 0", func(t *testing.T) {
		pos, err := adapter.GetLastPosition(ctx)

		require.NoError(t, err)
		assert.Equal(t, uint64(0), pos)
	})

	t.Run("returns last position", func(t *testing.T) {
		events := []adapters.EventRecord{{Type: "E1", Data: []byte(`{}`)}}
		stored, err := adapter.Append(ctx, "Order-pos1", events, mink.NoStream)
		require.NoError(t, err)

		pos, err := adapter.GetLastPosition(ctx)

		require.NoError(t, err)
		assert.Equal(t, stored[0].GlobalPosition, pos)
	})
}

func TestPostgresAdapter_Snapshots(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	defer cleanupSchema(t, db, schema)

	adapter := NewAdapterWithDB(db, WithSchema(schema))
	require.NoError(t, adapter.Initialize(context.Background()))

	ctx := context.Background()

	t.Run("save and load snapshot", func(t *testing.T) {
		data := []byte(`{"state":"test"}`)
		err := adapter.SaveSnapshot(ctx, "Order-snap", 5, data)
		require.NoError(t, err)

		snapshot, err := adapter.LoadSnapshot(ctx, "Order-snap")

		require.NoError(t, err)
		require.NotNil(t, snapshot)
		assert.Equal(t, "Order-snap", snapshot.StreamID)
		assert.Equal(t, int64(5), snapshot.Version)
		assert.Equal(t, data, snapshot.Data)
	})

	t.Run("update snapshot", func(t *testing.T) {
		err := adapter.SaveSnapshot(ctx, "Order-snap2", 1, []byte(`{}`))
		require.NoError(t, err)

		err = adapter.SaveSnapshot(ctx, "Order-snap2", 10, []byte(`{"updated":true}`))
		require.NoError(t, err)

		snapshot, err := adapter.LoadSnapshot(ctx, "Order-snap2")
		require.NoError(t, err)
		assert.Equal(t, int64(10), snapshot.Version)
	})

	t.Run("delete snapshot", func(t *testing.T) {
		err := adapter.SaveSnapshot(ctx, "Order-snap3", 1, []byte(`{}`))
		require.NoError(t, err)

		err = adapter.DeleteSnapshot(ctx, "Order-snap3")
		require.NoError(t, err)

		snapshot, err := adapter.LoadSnapshot(ctx, "Order-snap3")
		require.NoError(t, err)
		assert.Nil(t, snapshot)
	})
}

func TestPostgresAdapter_Checkpoints(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	defer cleanupSchema(t, db, schema)

	adapter := NewAdapterWithDB(db, WithSchema(schema))
	require.NoError(t, adapter.Initialize(context.Background()))

	ctx := context.Background()

	t.Run("set and get checkpoint", func(t *testing.T) {
		err := adapter.SetCheckpoint(ctx, "OrderProjection", 100)
		require.NoError(t, err)

		pos, err := adapter.GetCheckpoint(ctx, "OrderProjection")

		require.NoError(t, err)
		assert.Equal(t, uint64(100), pos)
	})

	t.Run("get non-existent checkpoint", func(t *testing.T) {
		pos, err := adapter.GetCheckpoint(ctx, "NonExistent")

		require.NoError(t, err)
		assert.Equal(t, uint64(0), pos)
	})

	t.Run("update checkpoint", func(t *testing.T) {
		err := adapter.SetCheckpoint(ctx, "UpdateProj", 50)
		require.NoError(t, err)

		err = adapter.SetCheckpoint(ctx, "UpdateProj", 150)
		require.NoError(t, err)

		pos, err := adapter.GetCheckpoint(ctx, "UpdateProj")
		require.NoError(t, err)
		assert.Equal(t, uint64(150), pos)
	})
}

func TestPostgresAdapter_Ping(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	adapter := NewAdapterWithDB(db)

	t.Run("ping healthy connection", func(t *testing.T) {
		err := adapter.Ping(context.Background())
		assert.NoError(t, err)
	})
}

func TestPostgresAdapter_Options(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	t.Run("custom schema", func(t *testing.T) {
		schema := fmt.Sprintf("custom_%d", time.Now().UnixNano())
		defer cleanupSchema(t, db, schema)

		adapter := NewAdapterWithDB(db, WithSchema(schema))
		assert.Equal(t, schema, adapter.Schema())

		err := adapter.Initialize(context.Background())
		require.NoError(t, err)
	})
}

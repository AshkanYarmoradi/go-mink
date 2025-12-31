package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
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

	// Wait for database to be ready
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for {
		err := db.PingContext(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("Database not ready: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	return db
}

// newTestAdapter creates a new adapter for testing.
// It requires a valid schema name and database connection.
func newTestAdapter(t *testing.T, db *sql.DB, opts ...Option) *PostgresAdapter {
	t.Helper()
	adapter, err := NewAdapterWithDB(db, opts...)
	require.NoError(t, err)
	return adapter
}

// cleanupSchema drops the test schema.
func cleanupSchema(t *testing.T, db *sql.DB, schema string) {
	_, err := db.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
	require.NoError(t, err)
}

// newTestSchema creates a unique test schema name.
func newTestSchema() string {
	return fmt.Sprintf("test_%d", time.Now().UnixNano())
}

func TestNewAdapter(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	t.Run("creates adapter with connection string", func(t *testing.T) {
		adapter, err := NewAdapter(connStr)
		require.NoError(t, err)
		require.NotNil(t, adapter)
		defer adapter.Close()

		assert.Equal(t, "mink", adapter.Schema())
		assert.NotNil(t, adapter.DB())
	})

	t.Run("creates adapter with options", func(t *testing.T) {
		adapter, err := NewAdapter(connStr,
			WithSchema("custom_schema"),
			WithMaxConnections(10),
			WithMaxIdleConnections(5),
			WithConnectionMaxLifetime(time.Hour),
		)
		require.NoError(t, err)
		require.NotNil(t, adapter)
		defer adapter.Close()

		assert.Equal(t, "custom_schema", adapter.Schema())
	})

	t.Run("returns error for invalid connection string", func(t *testing.T) {
		// pgx accepts most connection strings, so we need a truly invalid one
		adapter, err := NewAdapter("invalid://not-a-valid-url")
		if err == nil && adapter != nil {
			// If it connected, try to ping - that should fail
			err = adapter.Ping(context.Background())
			adapter.Close()
		}
		// Either connection fails or ping fails
		assert.Error(t, err)
	})
}

func TestNewAdapterWithDB(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	t.Run("creates adapter with existing connection", func(t *testing.T) {
		adapter, err := NewAdapterWithDB(db)
		require.NoError(t, err)
		require.NotNil(t, adapter)

		assert.Equal(t, "mink", adapter.Schema())
		assert.Equal(t, db, adapter.DB())
	})

	t.Run("applies options", func(t *testing.T) {
		adapter, err := NewAdapterWithDB(db, WithSchema("test_schema"))
		require.NoError(t, err)
		assert.Equal(t, "test_schema", adapter.Schema())
	})

	t.Run("rejects invalid schema name", func(t *testing.T) {
		_, err := NewAdapterWithDB(db, WithSchema("invalid-schema"))
		assert.ErrorIs(t, err, ErrInvalidSchemaName)
	})

	t.Run("rejects nil database", func(t *testing.T) {
		_, err := NewAdapterWithDB(nil)
		assert.Error(t, err)
	})
}

func TestPostgresAdapter_Initialize(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := newTestSchema()
	defer cleanupSchema(t, db, schema)

	adapter := newTestAdapter(t, db, WithSchema(schema))

	t.Run("creates schema and tables", func(t *testing.T) {
		err := adapter.Initialize(context.Background())
		require.NoError(t, err)

		// Verify tables exist
		tables := []string{"events", "streams", "snapshots", "checkpoints"}
		for _, table := range tables {
			var exists bool
			err = db.QueryRow(`
				SELECT EXISTS (
					SELECT FROM information_schema.tables 
					WHERE table_schema = $1 AND table_name = $2
				)`, schema, table).Scan(&exists)
			require.NoError(t, err)
			assert.True(t, exists, "table %s should exist", table)
		}
	})

	t.Run("idempotent initialization", func(t *testing.T) {
		err := adapter.Initialize(context.Background())
		require.NoError(t, err)

		err = adapter.Initialize(context.Background())
		require.NoError(t, err)
	})
}

func TestPostgresAdapter_MigrationVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	t.Run("returns 0 for uninitialized schema", func(t *testing.T) {
		schema := newTestSchema()
		defer cleanupSchema(t, db, schema)

		adapter := newTestAdapter(t, db, WithSchema(schema))

		version, err := adapter.MigrationVersion(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 0, version)
	})

	t.Run("returns 1 for initialized schema", func(t *testing.T) {
		schema := newTestSchema()
		defer cleanupSchema(t, db, schema)

		adapter := newTestAdapter(t, db, WithSchema(schema))
		require.NoError(t, adapter.Initialize(context.Background()))

		version, err := adapter.MigrationVersion(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 1, version)
	})
}

func TestPostgresAdapter_Append(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := newTestSchema()
	defer cleanupSchema(t, db, schema)

	adapter := newTestAdapter(t, db, WithSchema(schema))
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

	t.Run("append with AnyVersion", func(t *testing.T) {
		events := []adapters.EventRecord{{Type: "Test", Data: []byte(`{}`)}}

		// Should work on non-existent stream
		stored1, err := adapter.Append(ctx, "Order-any-1", events, mink.AnyVersion)
		require.NoError(t, err)
		assert.Equal(t, int64(1), stored1[0].Version)

		// Should work on existing stream
		stored2, err := adapter.Append(ctx, "Order-any-1", events, mink.AnyVersion)
		require.NoError(t, err)
		assert.Equal(t, int64(2), stored2[0].Version)
	})

	t.Run("append with StreamExists", func(t *testing.T) {
		// Should fail on non-existent stream
		events := []adapters.EventRecord{{Type: "Test", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "Order-exists-1", events, mink.StreamExists)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrStreamNotFound))

		// Create the stream
		_, err = adapter.Append(ctx, "Order-exists-1", events, mink.NoStream)
		require.NoError(t, err)

		// Should work on existing stream
		stored, err := adapter.Append(ctx, "Order-exists-1", events, mink.StreamExists)
		require.NoError(t, err)
		assert.Equal(t, int64(2), stored[0].Version)
	})

	t.Run("concurrency conflict - wrong version", func(t *testing.T) {
		// Create stream
		events1 := []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "Order-conflict", events1, mink.NoStream)
		require.NoError(t, err)

		// Try to append with wrong version
		events2 := []adapters.EventRecord{{Type: "ItemAdded", Data: []byte(`{}`)}}
		_, err = adapter.Append(ctx, "Order-conflict", events2, 0)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrConcurrencyConflict))

		// Verify it's a ConcurrencyError with details
		var concErr *ConcurrencyError
		if errors.As(err, &concErr) {
			assert.Equal(t, "Order-conflict", concErr.StreamID)
			assert.Equal(t, int64(0), concErr.ExpectedVersion)
			assert.Equal(t, int64(1), concErr.ActualVersion)
		}
	})

	t.Run("concurrency conflict - NoStream on existing", func(t *testing.T) {
		// Create stream
		events := []adapters.EventRecord{{Type: "Test", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "Order-nostream-conflict", events, mink.NoStream)
		require.NoError(t, err)

		// Try to create again with NoStream
		_, err = adapter.Append(ctx, "Order-nostream-conflict", events, mink.NoStream)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrConcurrencyConflict))
	})

	t.Run("invalid version number", func(t *testing.T) {
		events := []adapters.EventRecord{{Type: "Test", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "Order-invalid-ver", events, -5)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrInvalidVersion))
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
		assert.Equal(t, metadata.CausationID, stored[0].Metadata.CausationID)
		assert.Equal(t, metadata.UserID, stored[0].Metadata.UserID)
		assert.Equal(t, metadata.TenantID, stored[0].Metadata.TenantID)
		assert.Equal(t, "value", stored[0].Metadata.Custom["key"])
	})

	t.Run("empty stream ID", func(t *testing.T) {
		events := []adapters.EventRecord{{Type: "Test", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "", events, mink.NoStream)
		assert.True(t, errors.Is(err, adapters.ErrEmptyStreamID))
	})

	t.Run("no events", func(t *testing.T) {
		_, err := adapter.Append(ctx, "Order-empty", []adapters.EventRecord{}, mink.NoStream)
		assert.True(t, errors.Is(err, adapters.ErrNoEvents))
	})

	t.Run("concurrent appends to same stream", func(t *testing.T) {
		// Create stream first
		events := []adapters.EventRecord{{Type: "Init", Data: []byte(`{}`)}}
		_, err := adapter.Append(ctx, "Order-concurrent", events, mink.NoStream)
		require.NoError(t, err)

		// Run concurrent appends
		var wg sync.WaitGroup
		successCount := 0
		conflictCount := 0
		var mu sync.Mutex

		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				events := []adapters.EventRecord{{Type: fmt.Sprintf("Event-%d", i), Data: []byte(`{}`)}}
				_, err := adapter.Append(ctx, "Order-concurrent", events, 1) // All expect version 1

				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					successCount++
				} else if errors.Is(err, adapters.ErrConcurrencyConflict) {
					conflictCount++
				}
			}(i)
		}

		wg.Wait()

		// Only one should succeed, rest should conflict
		assert.Equal(t, 1, successCount)
		assert.Equal(t, 4, conflictCount)
	})
}

func TestPostgresAdapter_Load(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := newTestSchema()
	defer cleanupSchema(t, db, schema)

	adapter := newTestAdapter(t, db, WithSchema(schema))
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

	t.Run("load preserves metadata", func(t *testing.T) {
		metadata := adapters.Metadata{
			CorrelationID: "load-corr",
			UserID:        "load-user",
			Custom:        map[string]string{"loaded": "true"},
		}
		records := []adapters.EventRecord{
			{Type: "Test", Data: []byte(`{}`), Metadata: metadata},
		}
		_, err := adapter.Append(ctx, "Order-load-meta", records, mink.NoStream)
		require.NoError(t, err)

		events, err := adapter.Load(ctx, "Order-load-meta", 0)

		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "load-corr", events[0].Metadata.CorrelationID)
		assert.Equal(t, "load-user", events[0].Metadata.UserID)
		assert.Equal(t, "true", events[0].Metadata.Custom["loaded"])
	})

	t.Run("empty stream ID returns error", func(t *testing.T) {
		_, err := adapter.Load(ctx, "", 0)
		assert.True(t, errors.Is(err, adapters.ErrEmptyStreamID))
	})
}

func TestPostgresAdapter_GetStreamInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := newTestSchema()
	defer cleanupSchema(t, db, schema)

	adapter := newTestAdapter(t, db, WithSchema(schema))
	require.NoError(t, adapter.Initialize(context.Background()))

	ctx := context.Background()

	t.Run("stream not found", func(t *testing.T) {
		_, err := adapter.GetStreamInfo(ctx, "Order-notfound")

		assert.Error(t, err)
		assert.True(t, errors.Is(err, adapters.ErrStreamNotFound))

		// Verify it's a StreamNotFoundError with details
		var snfErr *StreamNotFoundError
		if errors.As(err, &snfErr) {
			assert.Equal(t, "Order-notfound", snfErr.StreamID)
		}
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
		assert.False(t, info.CreatedAt.IsZero())
		assert.False(t, info.UpdatedAt.IsZero())
	})

	t.Run("extracts category correctly", func(t *testing.T) {
		testCases := []struct {
			streamID         string
			expectedCategory string
		}{
			{"Order-cat-123", "Order"},
			{"User-cat-abc-def", "User"},
			{"SingleWordCat", "SingleWordCat"},
			{"Multi-cat-Part-Stream-123", "Multi"},
		}

		for _, tc := range testCases {
			events := []adapters.EventRecord{{Type: "Test", Data: []byte(`{}`)}}
			_, err := adapter.Append(ctx, tc.streamID, events, mink.NoStream)
			require.NoError(t, err)

			info, err := adapter.GetStreamInfo(ctx, tc.streamID)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedCategory, info.Category, "stream: %s", tc.streamID)
		}
	})
}

func TestPostgresAdapter_GetLastPosition(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := newTestSchema()
	defer cleanupSchema(t, db, schema)

	adapter := newTestAdapter(t, db, WithSchema(schema))
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

	t.Run("returns highest position after multiple appends", func(t *testing.T) {
		events := []adapters.EventRecord{
			{Type: "E1", Data: []byte(`{}`)},
			{Type: "E2", Data: []byte(`{}`)},
		}
		stored, err := adapter.Append(ctx, "Order-pos2", events, mink.NoStream)
		require.NoError(t, err)

		pos, err := adapter.GetLastPosition(ctx)

		require.NoError(t, err)
		assert.Equal(t, stored[1].GlobalPosition, pos)
	})
}

func TestPostgresAdapter_Snapshots(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := newTestSchema()
	defer cleanupSchema(t, db, schema)

	adapter := newTestAdapter(t, db, WithSchema(schema))
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

	t.Run("load nonexistent snapshot returns nil", func(t *testing.T) {
		snapshot, err := adapter.LoadSnapshot(ctx, "Order-nonexistent-snap")

		require.NoError(t, err)
		assert.Nil(t, snapshot)
	})

	t.Run("update snapshot", func(t *testing.T) {
		err := adapter.SaveSnapshot(ctx, "Order-snap2", 1, []byte(`{"v":1}`))
		require.NoError(t, err)

		err = adapter.SaveSnapshot(ctx, "Order-snap2", 10, []byte(`{"v":10}`))
		require.NoError(t, err)

		snapshot, err := adapter.LoadSnapshot(ctx, "Order-snap2")
		require.NoError(t, err)
		assert.Equal(t, int64(10), snapshot.Version)
		assert.Equal(t, []byte(`{"v":10}`), snapshot.Data)
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

	t.Run("delete nonexistent snapshot succeeds", func(t *testing.T) {
		err := adapter.DeleteSnapshot(ctx, "Order-nonexistent-snap")
		require.NoError(t, err)
	})
}

func TestPostgresAdapter_Checkpoints(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	schema := newTestSchema()
	defer cleanupSchema(t, db, schema)

	adapter := newTestAdapter(t, db, WithSchema(schema))
	require.NoError(t, adapter.Initialize(context.Background()))

	ctx := context.Background()

	t.Run("set and get checkpoint", func(t *testing.T) {
		err := adapter.SetCheckpoint(ctx, "OrderProjection", 100)
		require.NoError(t, err)

		pos, err := adapter.GetCheckpoint(ctx, "OrderProjection")

		require.NoError(t, err)
		assert.Equal(t, uint64(100), pos)
	})

	t.Run("get non-existent checkpoint returns 0", func(t *testing.T) {
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

func TestPostgresAdapter_Close(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	t.Run("close releases resources", func(t *testing.T) {
		adapter, err := NewAdapter(connStr)
		require.NoError(t, err)

		err = adapter.Close()
		require.NoError(t, err)

		// Operations should fail after close
		_, err = adapter.Load(context.Background(), "test", 0)
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))
	})

	t.Run("all operations fail after close", func(t *testing.T) {
		adapter, err := NewAdapter(connStr)
		require.NoError(t, err)
		_ = adapter.Close()

		ctx := context.Background()

		_, err = adapter.Append(ctx, "test", []adapters.EventRecord{{Type: "T", Data: []byte(`{}`)}}, 0)
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		_, err = adapter.Load(ctx, "test", 0)
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		_, err = adapter.GetStreamInfo(ctx, "test")
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		_, err = adapter.GetLastPosition(ctx)
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		err = adapter.SaveSnapshot(ctx, "test", 1, []byte(`{}`))
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		_, err = adapter.LoadSnapshot(ctx, "test")
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		err = adapter.DeleteSnapshot(ctx, "test")
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		_, err = adapter.GetCheckpoint(ctx, "test")
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		err = adapter.SetCheckpoint(ctx, "test", 1)
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))

		err = adapter.Ping(ctx)
		assert.True(t, errors.Is(err, adapters.ErrAdapterClosed))
	})
}

func TestPostgresAdapter_Ping(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	defer db.Close()

	adapter := newTestAdapter(t, db)

	t.Run("ping healthy connection", func(t *testing.T) {
		err := adapter.Ping(context.Background())
		assert.NoError(t, err)
	})

	t.Run("ping with timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := adapter.Ping(ctx)
		assert.NoError(t, err)
	})
}

func TestConcurrencyError(t *testing.T) {
	t.Run("error message", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", 5, 10)
		assert.Contains(t, err.Error(), "Order-123")
		assert.Contains(t, err.Error(), "5")
		assert.Contains(t, err.Error(), "10")
	})

	t.Run("Is checks", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", 5, 10)
		assert.True(t, errors.Is(err, ErrConcurrencyConflict))
		assert.True(t, errors.Is(err, adapters.ErrConcurrencyConflict))
		assert.False(t, errors.Is(err, ErrStreamNotFound))
	})
}

func TestStreamNotFoundError(t *testing.T) {
	t.Run("error message", func(t *testing.T) {
		err := NewStreamNotFoundError("Order-123")
		assert.Contains(t, err.Error(), "Order-123")
	})

	t.Run("Is checks", func(t *testing.T) {
		err := NewStreamNotFoundError("Order-123")
		assert.True(t, errors.Is(err, ErrStreamNotFound))
		assert.True(t, errors.Is(err, adapters.ErrStreamNotFound))
		assert.False(t, errors.Is(err, ErrConcurrencyConflict))
	})
}

func TestExtractCategory(t *testing.T) {
	tests := []struct {
		streamID string
		expected string
	}{
		{"Order-123", "Order"},
		{"User-abc", "User"},
		{"SingleWord", "SingleWord"},
		{"Multi-Part-ID", "Multi"},
		{"", ""},
		{"-StartsWithDash", ""},
	}

	for _, tc := range tests {
		t.Run(tc.streamID, func(t *testing.T) {
			result := extractCategory(tc.streamID)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// Benchmarks

func BenchmarkPostgresAdapter_Append(b *testing.B) {
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		b.Skip("TEST_DATABASE_URL not set")
	}

	db, _ := sql.Open("pgx", connStr)
	defer db.Close()

	schema := newTestSchema()
	defer func() {
		_, _ = db.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
	}()

	adapter, err := NewAdapterWithDB(db, WithSchema(schema))
	if err != nil {
		b.Fatal(err)
	}
	_ = adapter.Initialize(context.Background())

	ctx := context.Background()
	events := []adapters.EventRecord{{Type: "Test", Data: []byte(`{"key":"value"}`)}}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		streamID := fmt.Sprintf("Order-%d", i)
		_, _ = adapter.Append(ctx, streamID, events, mink.NoStream)
	}
}

func BenchmarkPostgresAdapter_Load(b *testing.B) {
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		b.Skip("TEST_DATABASE_URL not set")
	}

	db, _ := sql.Open("pgx", connStr)
	defer db.Close()

	schema := newTestSchema()
	defer func() {
		_, _ = db.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
	}()

	adapter, err := NewAdapterWithDB(db, WithSchema(schema))
	if err != nil {
		b.Fatal(err)
	}
	_ = adapter.Initialize(context.Background())

	ctx := context.Background()

	// Setup: create stream with events
	events := make([]adapters.EventRecord, 100)
	for i := range events {
		events[i] = adapters.EventRecord{Type: "Test", Data: []byte(`{}`)}
	}
	_, _ = adapter.Append(ctx, "Order-bench", events, mink.NoStream)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = adapter.Load(ctx, "Order-bench", 0)
	}
}

func TestPostgresAdapter_WithHealthCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	t.Run("enables periodic health checking", func(t *testing.T) {
		adapter, err := NewAdapter(connStr,
			WithHealthCheck(50*time.Millisecond),
		)
		require.NoError(t, err)
		require.NotNil(t, adapter)

		// Let health check run a few cycles
		time.Sleep(150 * time.Millisecond)

		// Verify adapter still works
		err = adapter.Ping(context.Background())
		require.NoError(t, err)

		// Close stops health check
		err = adapter.Close()
		require.NoError(t, err)
	})
}

func TestPostgresAdapter_Stats(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	adapter, err := NewAdapter(connStr)
	require.NoError(t, err)
	defer adapter.Close()

	t.Run("returns connection pool statistics", func(t *testing.T) {
		stats := adapter.Stats()

		// MaxOpenConnections should be the default (0 = unlimited) or a positive number
		assert.GreaterOrEqual(t, stats.MaxOpenConnections, 0)
	})

	t.Run("stats reflect pool usage", func(t *testing.T) {
		// Force a connection by pinging
		err := adapter.Ping(context.Background())
		require.NoError(t, err)

		stats := adapter.Stats()
		// After ping, we should have at least 1 open connection
		assert.GreaterOrEqual(t, stats.OpenConnections, 1)
	})
}

func TestPostgresAdapter_WithConnectionMaxIdleTime(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	t.Run("sets connection max idle time", func(t *testing.T) {
		adapter, err := NewAdapter(connStr,
			WithConnectionMaxIdleTime(5*time.Minute),
		)
		require.NoError(t, err)
		defer adapter.Close()

		// Verify adapter is functional
		err = adapter.Ping(context.Background())
		require.NoError(t, err)
	})
}

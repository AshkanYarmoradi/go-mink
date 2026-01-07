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

// Unit tests for quoting functions

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple identifier",
			input:    "users",
			expected: `"users"`,
		},
		{
			name:     "identifier with underscore",
			input:    "user_events",
			expected: `"user_events"`,
		},
		{
			name:     "identifier with numbers",
			input:    "events2024",
			expected: `"events2024"`,
		},
		{
			name:     "reserved word",
			input:    "select",
			expected: `"select"`,
		},
		{
			name:     "another reserved word",
			input:    "table",
			expected: `"table"`,
		},
		{
			name:     "identifier with double quote",
			input:    `my"table`,
			expected: `"my""table"`,
		},
		{
			name:     "identifier with multiple double quotes",
			input:    `a"b"c`,
			expected: `"a""b""c"`,
		},
		{
			name:     "empty string",
			input:    "",
			expected: `""`,
		},
		{
			name:     "identifier with spaces",
			input:    "my table",
			expected: `"my table"`,
		},
		{
			name:     "identifier with special characters",
			input:    "table-name",
			expected: `"table-name"`,
		},
		{
			name:     "uppercase identifier",
			input:    "MyTable",
			expected: `"MyTable"`,
		},
		{
			name:     "SQL injection attempt",
			input:    "users; DROP TABLE users; --",
			expected: `"users; DROP TABLE users; --"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := quoteIdentifier(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestQuoteQualifiedTable(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		table    string
		expected string
	}{
		{
			name:     "simple schema and table",
			schema:   "public",
			table:    "users",
			expected: `"public"."users"`,
		},
		{
			name:     "custom schema",
			schema:   "mink",
			table:    "events",
			expected: `"mink"."events"`,
		},
		{
			name:     "schema with underscore",
			schema:   "my_schema",
			table:    "my_table",
			expected: `"my_schema"."my_table"`,
		},
		{
			name:     "reserved words",
			schema:   "select",
			table:    "from",
			expected: `"select"."from"`,
		},
		{
			name:     "with double quotes",
			schema:   `my"schema`,
			table:    `my"table`,
			expected: `"my""schema"."my""table"`,
		},
		{
			name:     "SQL injection in schema",
			schema:   "public; DROP SCHEMA public; --",
			table:    "users",
			expected: `"public; DROP SCHEMA public; --"."users"`,
		},
		{
			name:     "SQL injection in table",
			schema:   "public",
			table:    "users; DROP TABLE users; --",
			expected: `"public"."users; DROP TABLE users; --"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := quoteQualifiedTable(tt.schema, tt.table)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSafeSchemaIdentifier(t *testing.T) {
	tests := []struct {
		name        string
		schema      string
		expected    string
		expectError bool
	}{
		{
			name:        "valid simple schema",
			schema:      "public",
			expected:    `"public"`,
			expectError: false,
		},
		{
			name:        "valid schema with underscore",
			schema:      "my_schema",
			expected:    `"my_schema"`,
			expectError: false,
		},
		{
			name:        "valid schema starting with underscore",
			schema:      "_private",
			expected:    `"_private"`,
			expectError: false,
		},
		{
			name:        "empty schema",
			schema:      "",
			expected:    "",
			expectError: true,
		},
		{
			name:        "schema with special characters",
			schema:      "my-schema",
			expected:    "",
			expectError: true,
		},
		{
			name:        "schema with spaces",
			schema:      "my schema",
			expected:    "",
			expectError: true,
		},
		{
			name:        "schema with double quotes",
			schema:      `my"schema`,
			expected:    "",
			expectError: true,
		},
		{
			name:        "SQL injection attempt",
			schema:      "public; DROP SCHEMA public; --",
			expected:    "",
			expectError: true,
		},
		{
			name:        "schema starting with number",
			schema:      "123schema",
			expected:    "",
			expectError: true,
		},
		{
			name:        "schema exceeding max length",
			schema:      "this_schema_name_is_way_too_long_and_exceeds_the_maximum_allowed_length_of_63_characters",
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := safeSchemaIdentifier(tt.schema)
			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

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
	schemaQ := quoteIdentifier(schema)
	_, err := db.Exec(`DROP SCHEMA IF EXISTS ` + schemaQ + ` CASCADE`)
	require.NoError(t, err)
}

// newTestSchema creates a unique test schema name.
// The generated names contain only alphanumeric characters and underscores,
// making them safe for use in SQL queries.
func newTestSchema() string {
	return fmt.Sprintf("test_%d", time.Now().UnixNano())
}

// setupIntegrationTest creates a test adapter with a unique schema and performs cleanup.
// It skips the test if Short() is true or TEST_DATABASE_URL is not set.
// Returns the adapter and automatically registers cleanup with t.Cleanup.
func setupIntegrationTest(t *testing.T, opts ...Option) *PostgresAdapter {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	db := getTestDB(t)
	schema := newTestSchema()

	// Register cleanup to run after test completes
	t.Cleanup(func() {
		cleanupSchema(t, db, schema)
		db.Close()
	})

	allOpts := append([]Option{WithSchema(schema)}, opts...)
	adapter := newTestAdapter(t, db, allOpts...)
	require.NoError(t, adapter.Initialize(context.Background()))

	return adapter
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

	t.Run("returns error for invalid schema name", func(t *testing.T) {
		adapter, err := NewAdapter(connStr, WithSchema("invalid-schema"))
		assert.ErrorIs(t, err, ErrInvalidSchemaName)
		assert.Nil(t, adapter)
	})

	t.Run("returns error for invalid schema with health check", func(t *testing.T) {
		adapter, err := NewAdapter(connStr,
			WithSchema("invalid-schema"),
			WithHealthCheck(time.Minute),
		)
		assert.ErrorIs(t, err, ErrInvalidSchemaName)
		assert.Nil(t, adapter)
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
	adapter := setupIntegrationTest(t)
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
	adapter := setupIntegrationTest(t)
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
	adapter := setupIntegrationTest(t)
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
	adapter := setupIntegrationTest(t)
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
	adapter := setupIntegrationTest(t)
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
	adapter := setupIntegrationTest(t)
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
			result := adapters.ExtractCategory(tc.streamID)
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
		schemaQ, err := safeSchemaIdentifier(schema)
		if err != nil {
			return
		}
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schemaQ + ` CASCADE`)
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
		schemaQ, err := safeSchemaIdentifier(schema)
		if err != nil {
			return
		}
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schemaQ + ` CASCADE`)
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

// ============================================================================
// CLI Support Functions Tests
// ============================================================================

func TestPostgresAdapter_ListStreams(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("returns empty list when no streams", func(t *testing.T) {
		streams, err := adapter.ListStreams(ctx, "", 10)
		require.NoError(t, err)
		// May have streams from other tests - just verify it works
		assert.NotNil(t, streams)
	})

	t.Run("returns streams after append", func(t *testing.T) {
		// Create a unique stream
		streamID := fmt.Sprintf("ListTest-%d", time.Now().UnixNano())
		events := []adapters.EventRecord{
			{Type: "TestEvent", Data: []byte(`{}`)},
		}
		_, err := adapter.Append(ctx, streamID, events, mink.NoStream)
		require.NoError(t, err)

		streams, err := adapter.ListStreams(ctx, "ListTest-", 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(streams), 1)

		// Find our stream
		found := false
		for _, s := range streams {
			if s.StreamID == streamID {
				found = true
				assert.Equal(t, int64(1), s.EventCount)
				assert.Equal(t, "TestEvent", s.LastEventType)
				break
			}
		}
		assert.True(t, found, "Expected to find created stream")
	})

	t.Run("respects limit", func(t *testing.T) {
		streams, err := adapter.ListStreams(ctx, "", 1)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(streams), 1)
	})
}

func TestPostgresAdapter_GetStreamEvents(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("returns events for stream", func(t *testing.T) {
		streamID := fmt.Sprintf("EventsTest-%d", time.Now().UnixNano())
		events := []adapters.EventRecord{
			{Type: "Event1", Data: []byte(`{"seq":1}`)},
			{Type: "Event2", Data: []byte(`{"seq":2}`)},
		}
		_, err := adapter.Append(ctx, streamID, events, mink.NoStream)
		require.NoError(t, err)

		result, err := adapter.GetStreamEvents(ctx, streamID, 0, 10)
		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, "Event1", result[0].Type)
		assert.Equal(t, "Event2", result[1].Type)
	})

	t.Run("respects offset and limit", func(t *testing.T) {
		streamID := fmt.Sprintf("EventsOffsetTest-%d", time.Now().UnixNano())
		events := []adapters.EventRecord{
			{Type: "E1", Data: []byte(`{}`)},
			{Type: "E2", Data: []byte(`{}`)},
			{Type: "E3", Data: []byte(`{}`)},
		}
		_, err := adapter.Append(ctx, streamID, events, mink.NoStream)
		require.NoError(t, err)

		result, err := adapter.GetStreamEvents(ctx, streamID, 1, 1)
		require.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Equal(t, "E2", result[0].Type)
	})
}

func TestPostgresAdapter_GetEventStoreStats(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("returns stats", func(t *testing.T) {
		stats, err := adapter.GetEventStoreStats(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, stats.TotalEvents, int64(0))
		assert.GreaterOrEqual(t, stats.TotalStreams, int64(0))
	})
}

func TestPostgresAdapter_ListProjections(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("returns projections list", func(t *testing.T) {
		// Create a checkpoint which creates a projection entry
		projName := fmt.Sprintf("TestProj-%d", time.Now().UnixNano())
		err := adapter.SetCheckpoint(ctx, projName, 42)
		require.NoError(t, err)

		projections, err := adapter.ListProjections(ctx)
		require.NoError(t, err)
		assert.NotNil(t, projections)

		// Find our projection
		found := false
		for _, p := range projections {
			if p.Name == projName {
				found = true
				assert.Equal(t, int64(42), p.Position)
				break
			}
		}
		assert.True(t, found)
	})
}

func TestPostgresAdapter_GetProjection(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("returns nil for non-existent projection", func(t *testing.T) {
		proj, err := adapter.GetProjection(ctx, "nonexistent-projection")
		require.NoError(t, err)
		assert.Nil(t, proj)
	})

	t.Run("returns projection info", func(t *testing.T) {
		projName := fmt.Sprintf("GetProjTest-%d", time.Now().UnixNano())
		err := adapter.SetCheckpoint(ctx, projName, 100)
		require.NoError(t, err)

		proj, err := adapter.GetProjection(ctx, projName)
		require.NoError(t, err)
		require.NotNil(t, proj)
		assert.Equal(t, projName, proj.Name)
		assert.Equal(t, int64(100), proj.Position)
	})
}

func TestPostgresAdapter_SetProjectionStatus(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("sets projection status", func(t *testing.T) {
		projName := fmt.Sprintf("StatusTest-%d", time.Now().UnixNano())
		err := adapter.SetCheckpoint(ctx, projName, 50)
		require.NoError(t, err)

		err = adapter.SetProjectionStatus(ctx, projName, "paused")
		require.NoError(t, err)

		proj, err := adapter.GetProjection(ctx, projName)
		require.NoError(t, err)
		assert.Equal(t, "paused", proj.Status)
	})
}

func TestPostgresAdapter_ResetProjectionCheckpoint(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("resets checkpoint to zero", func(t *testing.T) {
		projName := fmt.Sprintf("ResetTest-%d", time.Now().UnixNano())
		err := adapter.SetCheckpoint(ctx, projName, 200)
		require.NoError(t, err)

		err = adapter.ResetProjectionCheckpoint(ctx, projName)
		require.NoError(t, err)

		pos, err := adapter.GetCheckpoint(ctx, projName)
		require.NoError(t, err)
		assert.Equal(t, uint64(0), pos)
	})
}

func TestPostgresAdapter_GetTotalEventCount(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("returns event count", func(t *testing.T) {
		count, err := adapter.GetTotalEventCount(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, count, int64(0))
	})
}

func TestPostgresAdapter_Migrations(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("GetAppliedMigrations returns list", func(t *testing.T) {
		migrations, err := adapter.GetAppliedMigrations(ctx)
		require.NoError(t, err)
		assert.NotNil(t, migrations)
	})

	t.Run("RecordMigration and RemoveMigrationRecord", func(t *testing.T) {
		migName := fmt.Sprintf("test_migration_%d", time.Now().UnixNano())

		// Record
		err := adapter.RecordMigration(ctx, migName)
		require.NoError(t, err)

		// Verify recorded
		migrations, err := adapter.GetAppliedMigrations(ctx)
		require.NoError(t, err)
		assert.Contains(t, migrations, migName)

		// Remove
		err = adapter.RemoveMigrationRecord(ctx, migName)
		require.NoError(t, err)

		// Verify removed
		migrations, err = adapter.GetAppliedMigrations(ctx)
		require.NoError(t, err)
		assert.NotContains(t, migrations, migName)
	})
}

func TestPostgresAdapter_ExecuteSQL(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("executes SQL successfully", func(t *testing.T) {
		tableName := fmt.Sprintf("test_table_%d", time.Now().UnixNano())
		err := adapter.ExecuteSQL(ctx, fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (id INT)", tableName))
		require.NoError(t, err)

		// Cleanup
		_ = adapter.ExecuteSQL(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	})
}

func TestPostgresAdapter_GenerateSchema(t *testing.T) {
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

	t.Run("generates schema SQL", func(t *testing.T) {
		schema := adapter.GenerateSchema("test-project", "events", "snapshots", "outbox")
		assert.Contains(t, schema, "CREATE TABLE")
		assert.Contains(t, schema, "events")
		assert.Contains(t, schema, "test-project")
	})
}

func TestPostgresAdapter_GetDiagnosticInfo(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("returns diagnostic info", func(t *testing.T) {
		info, err := adapter.GetDiagnosticInfo(ctx)
		require.NoError(t, err)
		assert.True(t, info.Connected)
		assert.NotEmpty(t, info.Version)
	})
}

func TestPostgresAdapter_CheckSchema(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("checks schema", func(t *testing.T) {
		result, err := adapter.CheckSchema(ctx, "events")
		require.NoError(t, err)
		assert.True(t, result.TableExists)
		assert.GreaterOrEqual(t, result.EventCount, int64(0))
	})
}

func TestPostgresAdapter_GetProjectionHealth(t *testing.T) {
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

	ctx := context.Background()
	require.NoError(t, adapter.Initialize(ctx))

	t.Run("returns projection health", func(t *testing.T) {
		health, err := adapter.GetProjectionHealth(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, health.TotalProjections, int64(0))
	})

	t.Run("returns health with projections behind", func(t *testing.T) {
		// Create checkpoints table if it doesn't exist
		_, _ = adapter.db.ExecContext(ctx, `
			CREATE TABLE IF NOT EXISTS mink_checkpoints (
				name VARCHAR(255) PRIMARY KEY,
				position BIGINT NOT NULL DEFAULT 0,
				updated_at TIMESTAMPTZ DEFAULT NOW()
			)
		`)

		// Use a unique stream name
		streamID := fmt.Sprintf("health-test-stream-%d", time.Now().UnixNano())

		// Add some events first
		events := []adapters.EventRecord{
			{Type: "TestHealthEvent", Data: []byte(`{"test":"health"}`)},
		}
		_, err := adapter.Append(ctx, streamID, events, 0)
		require.NoError(t, err)

		// Create a checkpoint that is behind
		_, err = adapter.db.ExecContext(ctx,
			"INSERT INTO mink_checkpoints (name, position, updated_at) VALUES ($1, $2, NOW()) ON CONFLICT (name) DO UPDATE SET position = $2",
			"health_test_projection", 0)
		require.NoError(t, err)

		health, err := adapter.GetProjectionHealth(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, health.TotalProjections, int64(1))
		assert.NotEmpty(t, health.Message)
	})
}

// TestPostgresAdapter_ClosedAdapterErrors consolidates all "closed adapter" error tests
// to reduce code duplication. Each operation on a closed adapter should return ErrAdapterClosed.
func TestPostgresAdapter_ClosedAdapterErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()

	// Define test cases for each adapter method that should fail when closed
	tests := []struct {
		name string
		fn   func(*PostgresAdapter) error
	}{
		{"ListStreams", func(a *PostgresAdapter) error { _, err := a.ListStreams(ctx, "", 10); return err }},
		{"GetStreamEvents", func(a *PostgresAdapter) error { _, err := a.GetStreamEvents(ctx, "any", 0, 10); return err }},
		{"GetEventStoreStats", func(a *PostgresAdapter) error { _, err := a.GetEventStoreStats(ctx); return err }},
		{"ListProjections", func(a *PostgresAdapter) error { _, err := a.ListProjections(ctx); return err }},
		{"GetProjection", func(a *PostgresAdapter) error { _, err := a.GetProjection(ctx, "test"); return err }},
		{"SetProjectionStatus", func(a *PostgresAdapter) error { return a.SetProjectionStatus(ctx, "test", "paused") }},
		{"ResetProjectionCheckpoint", func(a *PostgresAdapter) error { return a.ResetProjectionCheckpoint(ctx, "test") }},
		{"GetTotalEventCount", func(a *PostgresAdapter) error { _, err := a.GetTotalEventCount(ctx); return err }},
		{"GetAppliedMigrations", func(a *PostgresAdapter) error { _, err := a.GetAppliedMigrations(ctx); return err }},
		{"RecordMigration", func(a *PostgresAdapter) error { return a.RecordMigration(ctx, "test") }},
		{"GetDiagnosticInfo", func(a *PostgresAdapter) error { _, err := a.GetDiagnosticInfo(ctx); return err }},
		{"CheckSchema", func(a *PostgresAdapter) error { _, err := a.CheckSchema(ctx, "events"); return err }},
		{"GetProjectionHealth", func(a *PostgresAdapter) error { _, err := a.GetProjectionHealth(ctx); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create and immediately close adapter
			closedAdapter, _ := NewAdapter(connStr)
			closedAdapter.Close()

			err := tt.fn(closedAdapter)
			assert.ErrorIs(t, err, ErrAdapterClosed)
		})
	}
}

func TestSubscriptionConfig(t *testing.T) {
	t.Run("newSubscriptionConfig with defaults", func(t *testing.T) {
		cfg := newSubscriptionConfig(nil)
		assert.Equal(t, defaultSubscriptionBuffer, cfg.bufferSize)
		assert.Equal(t, defaultPollInterval, cfg.pollInterval)
		assert.Equal(t, defaultMaxRetries, cfg.maxRetries)
		assert.Nil(t, cfg.onError)
	})

	t.Run("newSubscriptionConfig with empty options", func(t *testing.T) {
		cfg := newSubscriptionConfig([]adapters.SubscriptionOptions{})
		assert.Equal(t, defaultSubscriptionBuffer, cfg.bufferSize)
		assert.Equal(t, defaultPollInterval, cfg.pollInterval)
	})

	t.Run("newSubscriptionConfig with custom options", func(t *testing.T) {
		errorHandler := func(err error) {}
		opts := []adapters.SubscriptionOptions{
			{
				BufferSize:   200,
				PollInterval: time.Second * 5,
				OnError:      errorHandler,
			},
		}
		cfg := newSubscriptionConfig(opts)
		assert.Equal(t, 200, cfg.bufferSize)
		assert.Equal(t, time.Second*5, cfg.pollInterval)
		assert.NotNil(t, cfg.onError)
	})

	t.Run("newSubscriptionConfig ignores zero values", func(t *testing.T) {
		opts := []adapters.SubscriptionOptions{
			{
				BufferSize:   0,
				PollInterval: 0,
			},
		}
		cfg := newSubscriptionConfig(opts)
		assert.Equal(t, defaultSubscriptionBuffer, cfg.bufferSize)
		assert.Equal(t, defaultPollInterval, cfg.pollInterval)
	})

	t.Run("handleError with custom handler", func(t *testing.T) {
		var capturedErr error
		cfg := subscriptionConfig{
			onError: func(err error) {
				capturedErr = err
			},
		}
		testErr := errors.New("test error")
		cfg.handleError(testErr, "test context")
		assert.Equal(t, testErr, capturedErr)
	})

	t.Run("handleError without custom handler logs error", func(t *testing.T) {
		cfg := subscriptionConfig{}
		// This will log to stdout but won't panic
		cfg.handleError(errors.New("test error"), "test context")
		// No assertion needed - just verify it doesn't panic
	})
}

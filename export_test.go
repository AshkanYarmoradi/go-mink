package mink

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test event types for export tests
// =============================================================================

type exportCustomerCreated struct {
	CustomerID string `json:"customerId"`
	Name       string `json:"name"`
	Email      string `json:"email"`
}

type exportOrderPlaced struct {
	OrderID    string  `json:"orderId"`
	CustomerID string  `json:"customerId"`
	Amount     float64 `json:"amount"`
}

type exportPaymentReceived struct {
	PaymentID string  `json:"paymentId"`
	OrderID   string  `json:"orderId"`
	Amount    float64 `json:"amount"`
}

// =============================================================================
// Test helpers
// =============================================================================

func newExportTestStore(t *testing.T) (*EventStore, *memory.MemoryAdapter) {
	t.Helper()
	adapter := memory.NewAdapter()
	store := New(adapter)
	store.RegisterEvents(
		exportCustomerCreated{},
		exportOrderPlaced{},
		exportPaymentReceived{},
	)
	return store, adapter
}

func seedExportEvents(t *testing.T, ctx context.Context, store *EventStore) {
	t.Helper()

	// Customer stream for tenant-1
	err := store.Append(ctx, "Customer-cust-1", []interface{}{
		exportCustomerCreated{CustomerID: "cust-1", Name: "Alice", Email: "alice@example.com"},
	}, WithAppendMetadata(Metadata{TenantID: "tenant-1", UserID: "admin-1"}))
	require.NoError(t, err)

	// Order stream for tenant-1
	err = store.Append(ctx, "Order-ord-1", []interface{}{
		exportOrderPlaced{OrderID: "ord-1", CustomerID: "cust-1", Amount: 99.99},
	}, WithAppendMetadata(Metadata{TenantID: "tenant-1", UserID: "cust-1"}))
	require.NoError(t, err)

	// Customer stream for tenant-2
	err = store.Append(ctx, "Customer-cust-2", []interface{}{
		exportCustomerCreated{CustomerID: "cust-2", Name: "Bob", Email: "bob@example.com"},
	}, WithAppendMetadata(Metadata{TenantID: "tenant-2", UserID: "admin-2"}))
	require.NoError(t, err)

	// Payment stream for tenant-1
	err = store.Append(ctx, "Payment-pay-1", []interface{}{
		exportPaymentReceived{PaymentID: "pay-1", OrderID: "ord-1", Amount: 99.99},
	}, WithAppendMetadata(Metadata{TenantID: "tenant-1", UserID: "system"}))
	require.NoError(t, err)
}

// =============================================================================
// NewDataExporter tests
// =============================================================================

func TestNewDataExporter(t *testing.T) {
	store, _ := newExportTestStore(t)

	t.Run("creates exporter with defaults", func(t *testing.T) {
		exporter := NewDataExporter(store)

		require.NotNil(t, exporter)
		assert.Equal(t, store, exporter.store)
		assert.Equal(t, 1000, exporter.batchSize)
	})

	t.Run("applies options", func(t *testing.T) {
		logger := newTestLogger()
		exporter := NewDataExporter(store,
			WithExportBatchSize(500),
			WithExportLogger(logger),
		)

		assert.Equal(t, 500, exporter.batchSize)
		assert.Equal(t, logger, exporter.logger)
	})

	t.Run("ignores invalid batch size", func(t *testing.T) {
		exporter := NewDataExporter(store, WithExportBatchSize(0))
		assert.Equal(t, 1000, exporter.batchSize)

		exporter = NewDataExporter(store, WithExportBatchSize(-1))
		assert.Equal(t, 1000, exporter.batchSize)
	})

	t.Run("ignores nil logger", func(t *testing.T) {
		exporter := NewDataExporter(store, WithExportLogger(nil))
		assert.NotNil(t, exporter.logger)
	})
}

// =============================================================================
// Export validation tests
// =============================================================================

func TestDataExporter_Export_Validation(t *testing.T) {
	store, _ := newExportTestStore(t)
	exporter := NewDataExporter(store)
	ctx := context.Background()

	t.Run("returns error for empty subject ID", func(t *testing.T) {
		_, err := exporter.Export(ctx, ExportRequest{
			Streams: []string{"Customer-cust-1"},
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSubjectIDRequired)
	})

	t.Run("returns error when no streams or filter", func(t *testing.T) {
		_, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "user-1",
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNoExportSources)
	})
}

// =============================================================================
// Stream-based export tests
// =============================================================================

func TestDataExporter_Export_StreamBased(t *testing.T) {
	store, _ := newExportTestStore(t)
	ctx := context.Background()
	seedExportEvents(t, ctx, store)
	exporter := NewDataExporter(store)

	t.Run("exports events from single stream", func(t *testing.T) {
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1"},
		})

		require.NoError(t, err)
		assert.Equal(t, "cust-1", result.SubjectID)
		assert.Equal(t, 1, result.TotalEvents)
		assert.Equal(t, 0, result.RedactedCount)
		require.Len(t, result.Events, 1)
		assert.Equal(t, "Customer-cust-1", result.Events[0].StreamID)
		assert.Equal(t, "exportCustomerCreated", result.Events[0].EventType)
		assert.False(t, result.Events[0].Redacted)
		assert.NotNil(t, result.Events[0].Data)
		assert.NotZero(t, result.ExportedAt)

		// Verify deserialized data
		data, ok := result.Events[0].Data.(exportCustomerCreated)
		require.True(t, ok)
		assert.Equal(t, "Alice", data.Name)
		assert.Equal(t, "alice@example.com", data.Email)
	})

	t.Run("exports events from multiple streams", func(t *testing.T) {
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1", "Order-ord-1"},
		})

		require.NoError(t, err)
		assert.Equal(t, 2, result.TotalEvents)
		require.Len(t, result.Streams, 2)
	})

	t.Run("skips non-existent streams", func(t *testing.T) {
		logger := newTestLogger()
		exporter := NewDataExporter(store, WithExportLogger(logger))

		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1", "NonExistent-stream"},
		})

		require.NoError(t, err)
		assert.Equal(t, 1, result.TotalEvents)
	})

	t.Run("returns empty result for all non-existent streams", func(t *testing.T) {
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "nobody",
			Streams:   []string{"NonExistent-1", "NonExistent-2"},
		})

		require.NoError(t, err)
		assert.Equal(t, 0, result.TotalEvents)
		assert.Empty(t, result.Events)
	})

	t.Run("applies filter within streams", func(t *testing.T) {
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1", "Order-ord-1"},
			Filter:    FilterByEventTypes("exportOrderPlaced"),
		})

		require.NoError(t, err)
		assert.Equal(t, 1, result.TotalEvents)
		assert.Equal(t, "exportOrderPlaced", result.Events[0].EventType)
	})

	t.Run("applies FromTime filter excludes past events", func(t *testing.T) {
		// All events were created at roughly the same time, so future filter excludes all
		future := time.Now().Add(time.Hour)
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1"},
			FromTime:  &future,
		})

		require.NoError(t, err)
		assert.Equal(t, 0, result.TotalEvents)
	})

	t.Run("applies ToTime filter excludes future events", func(t *testing.T) {
		// ToTime set to the past excludes all events created now
		past := time.Now().Add(-time.Hour)
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1"},
			ToTime:    &past,
		})

		require.NoError(t, err)
		assert.Equal(t, 0, result.TotalEvents)
	})

	t.Run("applies ToTime filter includes past events", func(t *testing.T) {
		// ToTime set to the future includes all events created now
		future := time.Now().Add(time.Hour)
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1"},
			ToTime:    &future,
		})

		require.NoError(t, err)
		assert.Equal(t, 1, result.TotalEvents)
	})

	t.Run("exports metadata correctly", func(t *testing.T) {
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1"},
		})

		require.NoError(t, err)
		require.Len(t, result.Events, 1)
		assert.Equal(t, "tenant-1", result.Events[0].Metadata.TenantID)
		assert.Equal(t, DefaultSchemaVersion, result.Events[0].Metadata.SchemaVersion)
	})
}

// =============================================================================
// Scan-based export tests
// =============================================================================

func TestDataExporter_Export_ScanBased(t *testing.T) {
	store, _ := newExportTestStore(t)
	ctx := context.Background()
	seedExportEvents(t, ctx, store)
	exporter := NewDataExporter(store, WithExportBatchSize(2))

	t.Run("scans all events and filters by tenant", func(t *testing.T) {
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "tenant-1",
			Filter:    FilterByTenantID("tenant-1"),
		})

		require.NoError(t, err)
		assert.Equal(t, 3, result.TotalEvents)
		for _, e := range result.Events {
			assert.Equal(t, "tenant-1", e.Metadata.TenantID)
		}
	})

	t.Run("scans all events and filters by user", func(t *testing.T) {
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "admin-1",
			Filter:    FilterByUserID("admin-1"),
		})

		require.NoError(t, err)
		assert.Equal(t, 1, result.TotalEvents)
		assert.Equal(t, "Customer-cust-1", result.Events[0].StreamID)
	})

	t.Run("scans with combined filters", func(t *testing.T) {
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "tenant-1-orders",
			Filter: CombineFilters(
				FilterByTenantID("tenant-1"),
				FilterByEventTypes("exportOrderPlaced"),
			),
		})

		require.NoError(t, err)
		assert.Equal(t, 1, result.TotalEvents)
		assert.Equal(t, "exportOrderPlaced", result.Events[0].EventType)
	})

	t.Run("scans with stream prefix filter", func(t *testing.T) {
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "customer-data",
			Filter:    FilterByStreamPrefix("Customer-"),
		})

		require.NoError(t, err)
		assert.Equal(t, 2, result.TotalEvents) // cust-1 and cust-2
	})

	t.Run("scans with metadata filter", func(t *testing.T) {
		// Append an event with custom metadata
		err := store.Append(ctx, "Tagged-1", []interface{}{
			exportOrderPlaced{OrderID: "tagged", CustomerID: "c1", Amount: 1.0},
		}, WithAppendMetadata(Metadata{
			Custom: map[string]string{"department": "sales"},
		}))
		require.NoError(t, err)

		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "sales-dept",
			Filter:    FilterByMetadata("department", "sales"),
		})

		require.NoError(t, err)
		assert.Equal(t, 1, result.TotalEvents)
	})

	t.Run("returns empty result when no events match", func(t *testing.T) {
		result, err := exporter.Export(ctx, ExportRequest{
			SubjectID: "nobody",
			Filter:    FilterByTenantID("nonexistent-tenant"),
		})

		require.NoError(t, err)
		assert.Equal(t, 0, result.TotalEvents)
		assert.Empty(t, result.Events)
	})
}

// =============================================================================
// Scan-based export with unsupported adapter
// =============================================================================

func TestDataExporter_Export_ScanNotSupported(t *testing.T) {
	// Use a minimal adapter that doesn't implement SubscriptionAdapter
	adapter := &minimalExportAdapter{}
	store := New(adapter)
	exporter := NewDataExporter(store)
	ctx := context.Background()

	_, err := exporter.Export(ctx, ExportRequest{
		SubjectID: "user-1",
		Filter:    FilterByTenantID("t1"),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExportScanNotSupported)
}

// minimalExportAdapter implements only EventStoreAdapter (no SubscriptionAdapter).
type minimalExportAdapter struct{}

var _ adapters.EventStoreAdapter = (*minimalExportAdapter)(nil)

func (a *minimalExportAdapter) Append(_ context.Context, _ string, _ []adapters.EventRecord, _ int64) ([]adapters.StoredEvent, error) {
	return nil, nil
}
func (a *minimalExportAdapter) Load(_ context.Context, _ string, _ int64) ([]adapters.StoredEvent, error) {
	return nil, nil
}
func (a *minimalExportAdapter) GetStreamInfo(_ context.Context, _ string) (*adapters.StreamInfo, error) {
	return nil, nil
}
func (a *minimalExportAdapter) GetLastPosition(_ context.Context) (uint64, error) { return 0, nil }
func (a *minimalExportAdapter) Initialize(_ context.Context) error               { return nil }
func (a *minimalExportAdapter) Close() error                                     { return nil }

// =============================================================================
// ExportStream tests
// =============================================================================

func TestDataExporter_ExportStream(t *testing.T) {
	store, _ := newExportTestStore(t)
	ctx := context.Background()
	seedExportEvents(t, ctx, store)
	exporter := NewDataExporter(store)

	t.Run("streams events via handler", func(t *testing.T) {
		var collected []ExportedEvent
		err := exporter.ExportStream(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1", "Order-ord-1"},
		}, func(_ context.Context, event ExportedEvent) error {
			collected = append(collected, event)
			return nil
		})

		require.NoError(t, err)
		assert.Len(t, collected, 2)
	})

	t.Run("stops on handler error", func(t *testing.T) {
		handlerErr := errors.New("stop here")
		count := 0
		err := exporter.ExportStream(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1", "Order-ord-1"},
		}, func(_ context.Context, _ ExportedEvent) error {
			count++
			return handlerErr
		})

		assert.ErrorIs(t, err, handlerErr)
		assert.Equal(t, 1, count)
	})

	t.Run("returns error for nil handler", func(t *testing.T) {
		err := exporter.ExportStream(ctx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1"},
		}, nil)

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrExportFailed)
	})

	t.Run("validates request", func(t *testing.T) {
		err := exporter.ExportStream(ctx, ExportRequest{}, func(_ context.Context, _ ExportedEvent) error {
			return nil
		})

		assert.ErrorIs(t, err, ErrSubjectIDRequired)
	})
}

// =============================================================================
// Context cancellation tests
// =============================================================================

func TestDataExporter_Export_ContextCancellation(t *testing.T) {
	store, _ := newExportTestStore(t)
	ctx := context.Background()
	seedExportEvents(t, ctx, store)
	exporter := NewDataExporter(store)

	t.Run("respects context cancellation during stream export", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel() // Cancel immediately

		_, err := exporter.Export(cancelCtx, ExportRequest{
			SubjectID: "cust-1",
			Streams:   []string{"Customer-cust-1"},
		})

		assert.Error(t, err)
	})

	t.Run("respects context cancellation during scan export", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()

		_, err := exporter.Export(cancelCtx, ExportRequest{
			SubjectID: "cust-1",
			Filter:    FilterByTenantID("tenant-1"),
		})

		assert.Error(t, err)
	})

	t.Run("cancels between events in stream inner loop", func(t *testing.T) {
		// Append multiple events to a single stream so the inner loop iterates
		multiStore, _ := newExportTestStore(t)
		err := multiStore.Append(ctx, "Multi-1", []interface{}{
			exportCustomerCreated{CustomerID: "c1", Name: "A", Email: "a@b.com"},
			exportOrderPlaced{OrderID: "o1", CustomerID: "c1", Amount: 1.0},
		})
		require.NoError(t, err)

		cancelCtx, cancel := context.WithCancel(ctx)
		exp := NewDataExporter(multiStore)
		count := 0
		err = exp.ExportStream(cancelCtx, ExportRequest{
			SubjectID: "c1",
			Streams:   []string{"Multi-1"},
		}, func(_ context.Context, _ ExportedEvent) error {
			count++
			cancel() // Cancel after first event
			return nil
		})

		assert.Error(t, err)
		assert.Equal(t, 1, count)
	})

	t.Run("cancels between events in scan inner loop", func(t *testing.T) {
		// Use a fresh store with events
		scanStore, _ := newExportTestStore(t)
		err := scanStore.Append(ctx, "S-1", []interface{}{
			exportCustomerCreated{CustomerID: "c1", Name: "A", Email: "a@b.com"},
		})
		require.NoError(t, err)
		err = scanStore.Append(ctx, "S-2", []interface{}{
			exportOrderPlaced{OrderID: "o1", CustomerID: "c1", Amount: 1.0},
		})
		require.NoError(t, err)

		cancelCtx, cancel := context.WithCancel(ctx)
		exp := NewDataExporter(scanStore, WithExportBatchSize(10))
		count := 0
		err = exp.ExportStream(cancelCtx, ExportRequest{
			SubjectID: "c1",
			Filter: func(_ StoredEvent) bool {
				return true // Match all
			},
		}, func(_ context.Context, _ ExportedEvent) error {
			count++
			cancel() // Cancel after first event
			return nil
		})

		assert.Error(t, err)
		assert.Equal(t, 1, count)
	})
}

// =============================================================================
// Adapter error path tests
// =============================================================================

func TestDataExporter_Export_LoadRawError(t *testing.T) {
	t.Run("wraps non-stream-not-found errors", func(t *testing.T) {
		loadErr := errors.New("database connection lost")
		adapter := &errorExportAdapter{loadErr: loadErr}
		store := New(adapter)
		exporter := NewDataExporter(store)

		_, err := exporter.Export(context.Background(), ExportRequest{
			SubjectID: "user-1",
			Streams:   []string{"Stream-1"},
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrExportFailed)
		assert.ErrorIs(t, err, loadErr)
	})

	t.Run("skips streams with ErrStreamNotFound from adapter", func(t *testing.T) {
		adapter := &errorExportAdapter{loadErr: ErrStreamNotFound}
		store := New(adapter)
		exporter := NewDataExporter(store, WithExportLogger(newTestLogger()))

		result, err := exporter.Export(context.Background(), ExportRequest{
			SubjectID: "user-1",
			Streams:   []string{"Missing-1"},
		})

		require.NoError(t, err)
		assert.Equal(t, 0, result.TotalEvents)
	})
}

func TestDataExporter_Export_ScanLoadError(t *testing.T) {
	// Adapter implements SubscriptionAdapter but LoadFromPosition returns an error
	loadErr := errors.New("scan failure")
	adapter := &errorScanExportAdapter{loadErr: loadErr}
	store := New(adapter)
	exporter := NewDataExporter(store, WithExportBatchSize(10))

	_, err := exporter.Export(context.Background(), ExportRequest{
		SubjectID: "user-1",
		Filter:    FilterByTenantID("t1"),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExportFailed)
	assert.ErrorIs(t, err, loadErr)
}

// errorExportAdapter returns a configurable error from Load.
type errorExportAdapter struct {
	loadErr error
}

var _ adapters.EventStoreAdapter = (*errorExportAdapter)(nil)

func (a *errorExportAdapter) Append(_ context.Context, _ string, _ []adapters.EventRecord, _ int64) ([]adapters.StoredEvent, error) {
	return nil, nil
}
func (a *errorExportAdapter) Load(_ context.Context, _ string, _ int64) ([]adapters.StoredEvent, error) {
	return nil, a.loadErr
}
func (a *errorExportAdapter) GetStreamInfo(_ context.Context, _ string) (*adapters.StreamInfo, error) {
	return nil, nil
}
func (a *errorExportAdapter) GetLastPosition(_ context.Context) (uint64, error) { return 0, nil }
func (a *errorExportAdapter) Initialize(_ context.Context) error               { return nil }
func (a *errorExportAdapter) Close() error                                     { return nil }

// errorScanExportAdapter implements both EventStoreAdapter and SubscriptionAdapter,
// returning an error from LoadFromPosition.
type errorScanExportAdapter struct {
	loadErr error
}

var _ adapters.EventStoreAdapter = (*errorScanExportAdapter)(nil)
var _ adapters.SubscriptionAdapter = (*errorScanExportAdapter)(nil)

func (a *errorScanExportAdapter) Append(_ context.Context, _ string, _ []adapters.EventRecord, _ int64) ([]adapters.StoredEvent, error) {
	return nil, nil
}
func (a *errorScanExportAdapter) Load(_ context.Context, _ string, _ int64) ([]adapters.StoredEvent, error) {
	return nil, nil
}
func (a *errorScanExportAdapter) GetStreamInfo(_ context.Context, _ string) (*adapters.StreamInfo, error) {
	return nil, nil
}
func (a *errorScanExportAdapter) GetLastPosition(_ context.Context) (uint64, error) { return 0, nil }
func (a *errorScanExportAdapter) Initialize(_ context.Context) error               { return nil }
func (a *errorScanExportAdapter) Close() error                                     { return nil }

func (a *errorScanExportAdapter) LoadFromPosition(_ context.Context, _ uint64, _ int) ([]adapters.StoredEvent, error) {
	return nil, a.loadErr
}
func (a *errorScanExportAdapter) SubscribeAll(_ context.Context, _ uint64, _ ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	return nil, nil
}
func (a *errorScanExportAdapter) SubscribeStream(_ context.Context, _ string, _ int64, _ ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	return nil, nil
}
func (a *errorScanExportAdapter) SubscribeCategory(_ context.Context, _ string, _ uint64, _ ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	return nil, nil
}

// =============================================================================
// Filter tests
// =============================================================================

func TestExportFilters(t *testing.T) {
	event := StoredEvent{
		StreamID: "Customer-cust-1",
		Type:     "exportCustomerCreated",
		Metadata: Metadata{
			TenantID: "tenant-1",
			UserID:   "admin-1",
			Custom:   map[string]string{"region": "eu"},
		},
	}

	t.Run("FilterByTenantID matches", func(t *testing.T) {
		assert.True(t, FilterByTenantID("tenant-1")(event))
		assert.False(t, FilterByTenantID("tenant-2")(event))
	})

	t.Run("FilterByUserID matches", func(t *testing.T) {
		assert.True(t, FilterByUserID("admin-1")(event))
		assert.False(t, FilterByUserID("admin-2")(event))
	})

	t.Run("FilterByStreamPrefix matches", func(t *testing.T) {
		assert.True(t, FilterByStreamPrefix("Customer-")(event))
		assert.False(t, FilterByStreamPrefix("Order-")(event))
	})

	t.Run("FilterByMetadata matches", func(t *testing.T) {
		assert.True(t, FilterByMetadata("region", "eu")(event))
		assert.False(t, FilterByMetadata("region", "us")(event))
		assert.False(t, FilterByMetadata("missing", "val")(event))
	})

	t.Run("FilterByMetadata handles nil custom", func(t *testing.T) {
		e := StoredEvent{Metadata: Metadata{}}
		assert.False(t, FilterByMetadata("key", "val")(e))
	})

	t.Run("FilterByEventTypes matches", func(t *testing.T) {
		assert.True(t, FilterByEventTypes("exportCustomerCreated", "exportOrderPlaced")(event))
		assert.False(t, FilterByEventTypes("exportOrderPlaced")(event))
	})

	t.Run("CombineFilters requires all", func(t *testing.T) {
		combined := CombineFilters(
			FilterByTenantID("tenant-1"),
			FilterByStreamPrefix("Customer-"),
		)
		assert.True(t, combined(event))

		// Fails if any filter fails
		combined = CombineFilters(
			FilterByTenantID("tenant-1"),
			FilterByTenantID("tenant-2"),
		)
		assert.False(t, combined(event))
	})

	t.Run("CombineFilters with no filters matches all", func(t *testing.T) {
		assert.True(t, CombineFilters()(event))
	})
}

// =============================================================================
// Export error type tests
// =============================================================================

func TestExportError(t *testing.T) {
	t.Run("Is matches ErrExportFailed", func(t *testing.T) {
		err := NewExportError("user-1", errors.New("something broke"))
		assert.ErrorIs(t, err, ErrExportFailed)
	})

	t.Run("Unwrap returns cause", func(t *testing.T) {
		cause := errors.New("root cause")
		err := NewExportError("user-1", cause)
		assert.ErrorIs(t, err, cause)
	})

	t.Run("Error includes subject ID", func(t *testing.T) {
		err := NewExportError("user-1", errors.New("oops"))
		assert.Contains(t, err.Error(), "user-1")
		assert.Contains(t, err.Error(), "oops")
	})

	t.Run("Error without subject ID", func(t *testing.T) {
		err := NewExportError("", errors.New("oops"))
		assert.Contains(t, err.Error(), "export failed")
		assert.NotContains(t, err.Error(), `""`)
	})
}

// =============================================================================
// ProcessStoredEvent tests
// =============================================================================

func TestEventStore_ProcessStoredEvent(t *testing.T) {
	store, _ := newExportTestStore(t)
	ctx := context.Background()

	// Append an event
	err := store.Append(ctx, "Customer-cust-1", []interface{}{
		exportCustomerCreated{CustomerID: "cust-1", Name: "Alice", Email: "alice@example.com"},
	})
	require.NoError(t, err)

	// Load raw
	raw, err := store.LoadRaw(ctx, "Customer-cust-1", 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)

	// Process it
	event, err := store.ProcessStoredEvent(ctx, raw[0])
	require.NoError(t, err)

	data, ok := event.Data.(exportCustomerCreated)
	require.True(t, ok)
	assert.Equal(t, "Alice", data.Name)
	assert.Equal(t, "cust-1", data.CustomerID)
}

// =============================================================================
// Crypto-shredding export tests
// =============================================================================

// exportEncryptedEvent is an event with PII fields for crypto-shredding tests.
type exportEncryptedEvent struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

func newExportEncryptedStore(t *testing.T) (context.Context, *local.Provider, *EventStore) {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	provider, err := local.New(local.WithKey("export-key", key))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	adapter := memory.NewAdapter()
	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("export-key"),
		WithEncryptedFields("exportEncryptedEvent", "email"),
	)

	// No onDecryptionError handler — key revocation will surface as ErrKeyRevoked
	store := New(adapter, WithFieldEncryption(config))
	store.RegisterEvents(exportEncryptedEvent{})
	return context.Background(), provider, store
}

func TestDataExporter_Export_CryptoShredding(t *testing.T) {
	ctx, provider, store := newExportEncryptedStore(t)
	logger := newTestLogger()

	// Append encrypted event
	err := store.Append(ctx, "User-shred-export-1", []interface{}{
		exportEncryptedEvent{UserID: "u1", Name: "Alice", Email: "alice@secret.com"},
	})
	require.NoError(t, err)

	// Verify it works before revocation
	exporter := NewDataExporter(store, WithExportLogger(logger))
	result, err := exporter.Export(ctx, ExportRequest{
		SubjectID: "u1",
		Streams:   []string{"User-shred-export-1"},
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	assert.False(t, result.Events[0].Redacted)
	assert.NotNil(t, result.Events[0].Data)
	assert.Equal(t, 0, result.RedactedCount)

	data, ok := result.Events[0].Data.(exportEncryptedEvent)
	require.True(t, ok)
	assert.Equal(t, "alice@secret.com", data.Email)

	// Revoke the key (crypto-shredding)
	err = provider.RevokeKey("export-key")
	require.NoError(t, err)

	// Export again — events should be marked as redacted
	result, err = exporter.Export(ctx, ExportRequest{
		SubjectID: "u1",
		Streams:   []string{"User-shred-export-1"},
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 1)

	assert.True(t, result.Events[0].Redacted)
	assert.Nil(t, result.Events[0].Data)
	assert.Equal(t, 1, result.RedactedCount)
	assert.Equal(t, 1, result.TotalEvents)
	assert.Equal(t, "User-shred-export-1", result.Events[0].StreamID)
	assert.Equal(t, "exportEncryptedEvent", result.Events[0].EventType)
	// RawData should still contain the original (encrypted) bytes
	assert.NotEmpty(t, result.Events[0].RawData)
}

func TestDataExporter_ExportStream_CryptoShredding(t *testing.T) {
	ctx, provider, store := newExportEncryptedStore(t)

	// Append two events, one encrypted and one plain (non-encrypted type)
	err := store.Append(ctx, "User-shred-stream-1", []interface{}{
		exportEncryptedEvent{UserID: "u2", Name: "Bob", Email: "bob@secret.com"},
	})
	require.NoError(t, err)

	// Revoke the key
	err = provider.RevokeKey("export-key")
	require.NoError(t, err)

	// ExportStream should yield redacted events
	var collected []ExportedEvent
	exporter := NewDataExporter(store)
	err = exporter.ExportStream(ctx, ExportRequest{
		SubjectID: "u2",
		Streams:   []string{"User-shred-stream-1"},
	}, func(_ context.Context, event ExportedEvent) error {
		collected = append(collected, event)
		return nil
	})

	require.NoError(t, err)
	require.Len(t, collected, 1)
	assert.True(t, collected[0].Redacted)
	assert.Nil(t, collected[0].Data)
}

func TestDataExporter_Export_CryptoShredding_ScanBased(t *testing.T) {
	ctx, provider, store := newExportEncryptedStore(t)

	// Append encrypted event
	err := store.Append(ctx, "User-shred-scan-1", []interface{}{
		exportEncryptedEvent{UserID: "u3", Name: "Charlie", Email: "charlie@secret.com"},
	})
	require.NoError(t, err)

	// Revoke the key
	err = provider.RevokeKey("export-key")
	require.NoError(t, err)

	// Scan-based export with filter should yield redacted events
	exporter := NewDataExporter(store, WithExportBatchSize(10))
	result, err := exporter.Export(ctx, ExportRequest{
		SubjectID: "u3",
		Filter:    FilterByStreamPrefix("User-shred-scan-"),
	})

	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	assert.True(t, result.Events[0].Redacted)
	assert.Nil(t, result.Events[0].Data)
	assert.Equal(t, 1, result.RedactedCount)
}

func TestDataExporter_Export_ProcessStoredEventError(t *testing.T) {
	// Use an adapter that returns events with invalid JSON data,
	// exercising the generic error fallback path in processStoredEvent.
	adapter := &badDataExportAdapter{
		events: []adapters.StoredEvent{
			{
				ID:             "evt-1",
				StreamID:       "Bad-1",
				Type:           "SomeEvent",
				Data:           []byte(`{invalid json`),
				Version:        1,
				GlobalPosition: 1,
				Timestamp:      time.Now(),
			},
		},
	}
	store := New(adapter)
	exporter := NewDataExporter(store)

	_, err := exporter.Export(context.Background(), ExportRequest{
		SubjectID: "user-1",
		Streams:   []string{"Bad-1"},
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExportFailed)
}

// badDataExportAdapter returns pre-configured events with invalid data.
type badDataExportAdapter struct {
	events []adapters.StoredEvent
}

var _ adapters.EventStoreAdapter = (*badDataExportAdapter)(nil)

func (a *badDataExportAdapter) Append(_ context.Context, _ string, _ []adapters.EventRecord, _ int64) ([]adapters.StoredEvent, error) {
	return nil, nil
}
func (a *badDataExportAdapter) Load(_ context.Context, _ string, _ int64) ([]adapters.StoredEvent, error) {
	return a.events, nil
}
func (a *badDataExportAdapter) GetStreamInfo(_ context.Context, _ string) (*adapters.StreamInfo, error) {
	return nil, nil
}
func (a *badDataExportAdapter) GetLastPosition(_ context.Context) (uint64, error) { return 0, nil }
func (a *badDataExportAdapter) Initialize(_ context.Context) error               { return nil }
func (a *badDataExportAdapter) Close() error                                     { return nil }

func TestDataExporter_Export_DecryptionFailedRedacted(t *testing.T) {
	// Create a store with encryption configured but an adapter that returns
	// events with corrupted encryption metadata (invalid DEK).
	// This triggers the ErrDecryptionFailed branch in processStoredEvent.
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	provider, err := local.New(local.WithKey("test-key", key))
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	corruptAdapter := &badDataExportAdapter{
		events: []adapters.StoredEvent{
			{
				ID:       "evt-corrupt",
				StreamID: "Corrupt-1",
				Type:     "exportEncryptedEvent",
				Data:     []byte(`{"userId":"u1","name":"Alice","email":"encrypted-blob"}`),
				Metadata: adapters.Metadata{
					Custom: map[string]string{
						"$encrypted_fields":     `["email"]`,
						"$encryption_key_id":     "test-key",
						"$encrypted_dek":         "!!!not-valid-base64!!!",
						"$encryption_algorithm":  "AES-256-GCM",
					},
				},
				Version:        1,
				GlobalPosition: 1,
				Timestamp:      time.Now(),
			},
		},
	}

	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("test-key"),
		WithEncryptedFields("exportEncryptedEvent", "email"),
	)
	store := New(corruptAdapter, WithFieldEncryption(config))
	store.RegisterEvents(exportEncryptedEvent{})

	exporter := NewDataExporter(store)
	result, err := exporter.Export(context.Background(), ExportRequest{
		SubjectID: "u1",
		Streams:   []string{"Corrupt-1"},
	})

	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	assert.True(t, result.Events[0].Redacted)
	assert.Nil(t, result.Events[0].Data)
	assert.Equal(t, 1, result.RedactedCount)
}

func TestDataExporter_Export_ScanProcessStoredEventError(t *testing.T) {
	// badDataScanExportAdapter returns events with invalid JSON via LoadFromPosition
	// to trigger processStoredEvent error in the scan inner loop (lines 298-300).
	adapter := &badDataScanExportAdapter{
		events: []adapters.StoredEvent{
			{
				ID:             "evt-bad-scan",
				StreamID:       "Bad-Scan-1",
				Type:           "SomeEvent",
				Data:           []byte(`{invalid json`),
				Version:        1,
				GlobalPosition: 1,
				Timestamp:      time.Now(),
			},
		},
	}
	store := New(adapter)
	exporter := NewDataExporter(store, WithExportBatchSize(10))

	_, err := exporter.Export(context.Background(), ExportRequest{
		SubjectID: "user-1",
		Filter: func(_ StoredEvent) bool {
			return true // match all
		},
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExportFailed)
}

func TestDataExporter_ExportStream_ScanHandlerError(t *testing.T) {
	// Use a real store with events, then ExportStream with a scan filter
	// and a handler that returns an error (lines 302-304).
	store, _ := newExportTestStore(t)
	ctx := context.Background()
	seedExportEvents(t, ctx, store)

	exporter := NewDataExporter(store, WithExportBatchSize(10))
	handlerErr := errors.New("handler scan error")

	err := exporter.ExportStream(ctx, ExportRequest{
		SubjectID: "tenant-1",
		Filter:    FilterByTenantID("tenant-1"),
	}, func(_ context.Context, _ ExportedEvent) error {
		return handlerErr
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, handlerErr)
}

// badDataScanExportAdapter implements both EventStoreAdapter and SubscriptionAdapter,
// returning events with invalid data from LoadFromPosition.
type badDataScanExportAdapter struct {
	events []adapters.StoredEvent
}

var _ adapters.EventStoreAdapter = (*badDataScanExportAdapter)(nil)
var _ adapters.SubscriptionAdapter = (*badDataScanExportAdapter)(nil)

func (a *badDataScanExportAdapter) Append(_ context.Context, _ string, _ []adapters.EventRecord, _ int64) ([]adapters.StoredEvent, error) {
	return nil, nil
}
func (a *badDataScanExportAdapter) Load(_ context.Context, _ string, _ int64) ([]adapters.StoredEvent, error) {
	return nil, nil
}
func (a *badDataScanExportAdapter) GetStreamInfo(_ context.Context, _ string) (*adapters.StreamInfo, error) {
	return nil, nil
}
func (a *badDataScanExportAdapter) GetLastPosition(_ context.Context) (uint64, error) { return 0, nil }
func (a *badDataScanExportAdapter) Initialize(_ context.Context) error               { return nil }
func (a *badDataScanExportAdapter) Close() error                                     { return nil }

func (a *badDataScanExportAdapter) LoadFromPosition(_ context.Context, _ uint64, _ int) ([]adapters.StoredEvent, error) {
	events := a.events
	a.events = nil // Return empty on subsequent calls to break the loop
	return events, nil
}
func (a *badDataScanExportAdapter) SubscribeAll(_ context.Context, _ uint64, _ ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	return nil, nil
}
func (a *badDataScanExportAdapter) SubscribeStream(_ context.Context, _ string, _ int64, _ ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	return nil, nil
}
func (a *badDataScanExportAdapter) SubscribeCategory(_ context.Context, _ string, _ uint64, _ ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	return nil, nil
}

func TestDataExporter_Export_MixedRedactedAndPlain(t *testing.T) {
	ctx, provider, store := newExportEncryptedStore(t)
	// Also register a non-encrypted event type
	store.RegisterEvents(exportOrderPlaced{})

	// Append encrypted event
	err := store.Append(ctx, "User-mixed-1", []interface{}{
		exportEncryptedEvent{UserID: "u4", Name: "Diana", Email: "diana@secret.com"},
	})
	require.NoError(t, err)

	// Append non-encrypted event to a different stream
	err = store.Append(ctx, "Order-mixed-1", []interface{}{
		exportOrderPlaced{OrderID: "o1", CustomerID: "u4", Amount: 50.0},
	})
	require.NoError(t, err)

	// Revoke the key — only encrypted events should be redacted
	err = provider.RevokeKey("export-key")
	require.NoError(t, err)

	exporter := NewDataExporter(store)
	result, err := exporter.Export(ctx, ExportRequest{
		SubjectID: "u4",
		Streams:   []string{"User-mixed-1", "Order-mixed-1"},
	})

	require.NoError(t, err)
	assert.Equal(t, 2, result.TotalEvents)
	assert.Equal(t, 1, result.RedactedCount)

	// Find the redacted and non-redacted events
	for _, e := range result.Events {
		if e.StreamID == "User-mixed-1" {
			assert.True(t, e.Redacted, "encrypted event should be redacted")
			assert.Nil(t, e.Data)
		} else {
			assert.False(t, e.Redacted, "non-encrypted event should not be redacted")
			assert.NotNil(t, e.Data)
			order, ok := e.Data.(exportOrderPlaced)
			require.True(t, ok)
			assert.Equal(t, 50.0, order.Amount)
		}
	}
}

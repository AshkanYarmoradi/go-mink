package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// =============================================================================
// Test Types
// =============================================================================

type testCommand struct {
	id   string
	fail bool
}

func (c *testCommand) AggregateID() string   { return c.id }
func (c *testCommand) AggregateType() string { return "Test" }
func (c *testCommand) CommandType() string   { return "TestCommand" }
func (c *testCommand) Validate() error {
	if c.fail {
		return errors.New("validation failed")
	}
	return nil
}

type mockAdapter struct {
	appendErr error
	loadErr   error
	events    []adapters.StoredEvent
}

func (m *mockAdapter) Append(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
	if m.appendErr != nil {
		return nil, m.appendErr
	}
	stored := make([]adapters.StoredEvent, len(events))
	for i, e := range events {
		stored[i] = adapters.StoredEvent{
			ID:             "event-" + e.Type,
			StreamID:       streamID,
			Type:           e.Type,
			Data:           e.Data,
			Version:        int64(i + 1),
			GlobalPosition: uint64(i + 1),
			Timestamp:      time.Now(),
		}
	}
	return stored, nil
}

func (m *mockAdapter) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return m.events, nil
}

func (m *mockAdapter) GetStreamInfo(ctx context.Context, streamID string) (*adapters.StreamInfo, error) {
	return &adapters.StreamInfo{
		StreamID:   streamID,
		Version:    int64(len(m.events)),
		EventCount: int64(len(m.events)),
	}, nil
}

func (m *mockAdapter) GetLastPosition(ctx context.Context) (uint64, error) {
	if len(m.events) == 0 {
		return 0, nil
	}
	return m.events[len(m.events)-1].GlobalPosition, nil
}

func (m *mockAdapter) Initialize(ctx context.Context) error {
	return nil
}

func (m *mockAdapter) Close() error {
	return nil
}

type mockProjection struct {
	name     string
	events   []string
	applyErr error
	applied  []mink.StoredEvent
}

func (p *mockProjection) Name() string {
	return p.name
}

func (p *mockProjection) HandledEvents() []string {
	return p.events
}

func (p *mockProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
	p.applied = append(p.applied, event)
	return p.applyErr
}

// Ensure mockProjection implements mink.InlineProjection
var _ mink.InlineProjection = (*mockProjection)(nil)

// Ensure mockAdapter implements adapters.EventStoreAdapter
var _ adapters.EventStoreAdapter = (*mockAdapter)(nil)

// =============================================================================
// Metrics Tests
// =============================================================================

func TestNew(t *testing.T) {
	t.Run("creates metrics with defaults", func(t *testing.T) {
		m := New()

		assert.NotNil(t, m)
		assert.Equal(t, "mink", m.namespace)
		assert.Equal(t, "unknown", m.serviceName)
	})

	t.Run("with custom options", func(t *testing.T) {
		m := New(
			WithNamespace("custom"),
			WithSubsystem("events"),
			WithMetricsServiceName("order-service"),
		)

		assert.Equal(t, "custom", m.namespace)
		assert.Equal(t, "events", m.subsystem)
		assert.Equal(t, "order-service", m.serviceName)
	})
}

func TestMetrics_Collectors(t *testing.T) {
	t.Run("returns all collectors", func(t *testing.T) {
		m := New()
		collectors := m.Collectors()

		// Should have 12 collectors
		assert.Len(t, collectors, 12)
	})
}

func TestMetrics_Register(t *testing.T) {
	t.Run("registers with custom registry", func(t *testing.T) {
		m := New(WithNamespace("test_register"))
		registry := prometheus.NewRegistry()

		err := m.Register(registry)

		require.NoError(t, err)
	})

	t.Run("returns error on duplicate registration", func(t *testing.T) {
		m := New(WithNamespace("test_dup"))
		registry := prometheus.NewRegistry()

		err := m.Register(registry)
		require.NoError(t, err)

		err = m.Register(registry)
		require.Error(t, err)
	})
}

// =============================================================================
// Command Middleware Tests
// =============================================================================

func TestMetrics_CommandMiddleware(t *testing.T) {
	t.Run("records successful command", func(t *testing.T) {
		m := New(WithNamespace("cmd_success"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		middleware := m.CommandMiddleware()
		cmd := &testCommand{id: "test-123"}

		handler := middleware(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			return mink.NewSuccessResult("test-123", 1), nil
		})

		result, err := handler(context.Background(), cmd)

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())

		// Verify metrics
		count := testutil.ToFloat64(m.commandsTotal.WithLabelValues("test", "TestCommand", StatusSuccess))
		assert.Equal(t, float64(1), count)
	})

	t.Run("records failed command", func(t *testing.T) {
		m := New(WithNamespace("cmd_fail"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		middleware := m.CommandMiddleware()
		cmd := &testCommand{id: "test-123"}
		expectedErr := errors.New("command failed")

		handler := middleware(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			return mink.NewErrorResult(expectedErr), expectedErr
		})

		result, err := handler(context.Background(), cmd)

		require.Error(t, err)
		assert.False(t, result.IsSuccess())

		// Verify error metrics
		count := testutil.ToFloat64(m.commandsTotal.WithLabelValues("test", "TestCommand", StatusError))
		assert.Equal(t, float64(1), count)
	})

	t.Run("tracks in-flight commands", func(t *testing.T) {
		m := New(WithNamespace("cmd_inflight"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		middleware := m.CommandMiddleware()
		cmd := &testCommand{id: "test-123"}

		inFlightDuringExecution := float64(-1)

		handler := middleware(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			// Capture in-flight during execution
			inFlightDuringExecution = testutil.ToFloat64(m.commandsInFlight.WithLabelValues("test", "TestCommand"))
			return mink.NewSuccessResult("test-123", 1), nil
		})

		handler(context.Background(), cmd)

		// Should be 1 during execution
		assert.Equal(t, float64(1), inFlightDuringExecution)

		// Should be 0 after execution
		inFlightAfter := testutil.ToFloat64(m.commandsInFlight.WithLabelValues("test", "TestCommand"))
		assert.Equal(t, float64(0), inFlightAfter)
	})
}

// =============================================================================
// Event Store Middleware Tests
// =============================================================================

func TestEventStoreMiddleware_Append(t *testing.T) {
	t.Run("records successful append", func(t *testing.T) {
		m := New(WithNamespace("es_append_success"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		adapter := &mockAdapter{}
		middleware := m.WrapEventStore(adapter)

		events := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte("{}")},
			{Type: "ItemAdded", Data: []byte("{}")},
		}

		stored, err := middleware.Append(context.Background(), "order-123", events, mink.NoStream)

		require.NoError(t, err)
		assert.Len(t, stored, 2)

		// Verify metrics
		successCount := testutil.ToFloat64(m.eventStoreOperationsTotal.WithLabelValues("test", OperationAppend, StatusSuccess))
		assert.Equal(t, float64(1), successCount)

		orderCreatedCount := testutil.ToFloat64(m.eventsAppendedTotal.WithLabelValues("test", "OrderCreated"))
		assert.Equal(t, float64(1), orderCreatedCount)

		itemAddedCount := testutil.ToFloat64(m.eventsAppendedTotal.WithLabelValues("test", "ItemAdded"))
		assert.Equal(t, float64(1), itemAddedCount)
	})

	t.Run("records failed append", func(t *testing.T) {
		m := New(WithNamespace("es_append_fail"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		adapter := &mockAdapter{appendErr: errors.New("append failed")}
		middleware := m.WrapEventStore(adapter)

		_, err := middleware.Append(context.Background(), "order-123", []adapters.EventRecord{}, mink.NoStream)

		require.Error(t, err)

		// Verify error metrics
		errorCount := testutil.ToFloat64(m.eventStoreOperationsTotal.WithLabelValues("test", OperationAppend, StatusError))
		assert.Equal(t, float64(1), errorCount)

		appendErrorCount := testutil.ToFloat64(m.errorsTotal.WithLabelValues("test", "append_error"))
		assert.Equal(t, float64(1), appendErrorCount)
	})
}

func TestEventStoreMiddleware_Load(t *testing.T) {
	t.Run("records successful load", func(t *testing.T) {
		m := New(WithNamespace("es_load_success"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		adapter := &mockAdapter{
			events: []adapters.StoredEvent{
				{ID: "event-1", Type: "OrderCreated"},
				{ID: "event-2", Type: "ItemAdded"},
			},
		}
		middleware := m.WrapEventStore(adapter)

		events, err := middleware.Load(context.Background(), "order-123", 0)

		require.NoError(t, err)
		assert.Len(t, events, 2)

		// Verify metrics
		successCount := testutil.ToFloat64(m.eventStoreOperationsTotal.WithLabelValues("test", OperationLoad, StatusSuccess))
		assert.Equal(t, float64(1), successCount)

		loadedCount := testutil.ToFloat64(m.eventsLoadedTotal.WithLabelValues("test"))
		assert.Equal(t, float64(2), loadedCount)
	})

	t.Run("records failed load", func(t *testing.T) {
		m := New(WithNamespace("es_load_fail"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		adapter := &mockAdapter{loadErr: errors.New("load failed")}
		middleware := m.WrapEventStore(adapter)

		_, err := middleware.Load(context.Background(), "order-123", 0)

		require.Error(t, err)

		// Verify error metrics
		errorCount := testutil.ToFloat64(m.eventStoreOperationsTotal.WithLabelValues("test", OperationLoad, StatusError))
		assert.Equal(t, float64(1), errorCount)
	})
}

func TestEventStoreMiddleware_GetStreamInfo(t *testing.T) {
	t.Run("records successful get stream info", func(t *testing.T) {
		m := New(WithNamespace("es_info_success"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		adapter := &mockAdapter{
			events: []adapters.StoredEvent{
				{ID: "event-1", Type: "OrderCreated", Version: 1},
				{ID: "event-2", Type: "ItemAdded", Version: 2},
			},
		}
		middleware := m.WrapEventStore(adapter)

		info, err := middleware.GetStreamInfo(context.Background(), "order-123")

		require.NoError(t, err)
		assert.Equal(t, "order-123", info.StreamID)
	})
}

func TestEventStoreMiddleware_GetLastPosition(t *testing.T) {
	t.Run("records successful get last position", func(t *testing.T) {
		m := New(WithNamespace("es_pos_success"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		adapter := &mockAdapter{
			events: []adapters.StoredEvent{
				{ID: "event-1", GlobalPosition: 100},
			},
		}
		middleware := m.WrapEventStore(adapter)

		pos, err := middleware.GetLastPosition(context.Background())

		require.NoError(t, err)
		assert.Equal(t, uint64(100), pos)
	})
}

func TestEventStoreMiddleware_Initialize(t *testing.T) {
	t.Run("records successful initialize", func(t *testing.T) {
		m := New(WithNamespace("es_init_success"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		adapter := &mockAdapter{}
		middleware := m.WrapEventStore(adapter)

		err := middleware.Initialize(context.Background())

		require.NoError(t, err)
	})
}

// =============================================================================
// Projection Middleware Tests
// =============================================================================

func TestProjectionMiddleware(t *testing.T) {
	t.Run("Name delegates to underlying projection", func(t *testing.T) {
		m := New()
		projection := &mockProjection{name: "TestProjection"}
		middleware := m.WrapProjection(projection)

		assert.Equal(t, "TestProjection", middleware.Name())
	})

	t.Run("HandledEvents delegates to underlying projection", func(t *testing.T) {
		m := New()
		projection := &mockProjection{events: []string{"Event1", "Event2"}}
		middleware := m.WrapProjection(projection)

		assert.Equal(t, []string{"Event1", "Event2"}, middleware.HandledEvents())
	})

	t.Run("records successful apply", func(t *testing.T) {
		m := New(WithNamespace("proj_success"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		projection := &mockProjection{name: "OrderProjection"}
		middleware := m.WrapProjection(projection)

		event := mink.StoredEvent{
			ID:             "event-123",
			StreamID:       "order-123",
			Type:           "OrderCreated",
			Version:        1,
			GlobalPosition: 100,
		}

		err := middleware.Apply(context.Background(), event)

		require.NoError(t, err)

		// Verify metrics
		successCount := testutil.ToFloat64(m.projectionsProcessedTotal.WithLabelValues("test", "OrderProjection", "OrderCreated", StatusSuccess))
		assert.Equal(t, float64(1), successCount)

		checkpoint := testutil.ToFloat64(m.projectionCheckpoint.WithLabelValues("test", "OrderProjection"))
		assert.Equal(t, float64(100), checkpoint)
	})

	t.Run("records failed apply", func(t *testing.T) {
		m := New(WithNamespace("proj_fail"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		projection := &mockProjection{
			name:     "OrderProjection",
			applyErr: errors.New("apply failed"),
		}
		middleware := m.WrapProjection(projection)

		event := mink.StoredEvent{
			ID:   "event-123",
			Type: "OrderCreated",
		}

		err := middleware.Apply(context.Background(), event)

		require.Error(t, err)

		// Verify error metrics
		errorCount := testutil.ToFloat64(m.projectionsProcessedTotal.WithLabelValues("test", "OrderProjection", "OrderCreated", StatusError))
		assert.Equal(t, float64(1), errorCount)
	})
}

// =============================================================================
// Manual Recording Tests
// =============================================================================

func TestMetrics_RecordProjectionLag(t *testing.T) {
	t.Run("records lag", func(t *testing.T) {
		m := New(WithNamespace("lag_test"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		m.RecordProjectionLag("OrderProjection", 50)

		lag := testutil.ToFloat64(m.projectionLag.WithLabelValues("test", "OrderProjection"))
		assert.Equal(t, float64(50), lag)
	})
}

func TestMetrics_RecordProjectionCheckpoint(t *testing.T) {
	t.Run("records checkpoint", func(t *testing.T) {
		m := New(WithNamespace("cp_test"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		m.RecordProjectionCheckpoint("OrderProjection", 1000)

		checkpoint := testutil.ToFloat64(m.projectionCheckpoint.WithLabelValues("test", "OrderProjection"))
		assert.Equal(t, float64(1000), checkpoint)
	})
}

func TestMetrics_RecordError(t *testing.T) {
	t.Run("records custom error", func(t *testing.T) {
		m := New(WithNamespace("err_test"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		m.Register(registry)

		m.RecordError("custom_error")
		m.RecordError("custom_error")

		errorCount := testutil.ToFloat64(m.errorsTotal.WithLabelValues("test", "custom_error"))
		assert.Equal(t, float64(2), errorCount)
	})
}

// =============================================================================
// Getter Tests
// =============================================================================

func TestMetrics_Getters(t *testing.T) {
	m := New()

	t.Run("CommandsTotal returns counter", func(t *testing.T) {
		assert.NotNil(t, m.CommandsTotal())
	})

	t.Run("CommandDuration returns histogram", func(t *testing.T) {
		assert.NotNil(t, m.CommandDuration())
	})

	t.Run("CommandsInFlight returns gauge", func(t *testing.T) {
		assert.NotNil(t, m.CommandsInFlight())
	})

	t.Run("EventStoreOperationsTotal returns counter", func(t *testing.T) {
		assert.NotNil(t, m.EventStoreOperationsTotal())
	})

	t.Run("EventStoreOperationDuration returns histogram", func(t *testing.T) {
		assert.NotNil(t, m.EventStoreOperationDuration())
	})

	t.Run("EventsAppendedTotal returns counter", func(t *testing.T) {
		assert.NotNil(t, m.EventsAppendedTotal())
	})

	t.Run("EventsLoadedTotal returns counter", func(t *testing.T) {
		assert.NotNil(t, m.EventsLoadedTotal())
	})

	t.Run("ProjectionsProcessedTotal returns counter", func(t *testing.T) {
		assert.NotNil(t, m.ProjectionsProcessedTotal())
	})

	t.Run("ProjectionDuration returns histogram", func(t *testing.T) {
		assert.NotNil(t, m.ProjectionDuration())
	})

	t.Run("ProjectionLag returns gauge", func(t *testing.T) {
		assert.NotNil(t, m.ProjectionLag())
	})

	t.Run("ProjectionCheckpoint returns gauge", func(t *testing.T) {
		assert.NotNil(t, m.ProjectionCheckpoint())
	})

	t.Run("ErrorsTotal returns counter", func(t *testing.T) {
		assert.NotNil(t, m.ErrorsTotal())
	})
}

package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters"
	minktest "github.com/AshkanYarmoradi/go-mink/testing/testutil"
)

// Ensure MockProjection implements mink.InlineProjection
var _ mink.InlineProjection = (*minktest.MockProjection)(nil)

// Ensure MockAdapter implements adapters.EventStoreAdapter
var _ adapters.EventStoreAdapter = (*minktest.MockAdapter)(nil)

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
		_ = m.Register(registry)

		middleware := m.CommandMiddleware()
		cmd := &minktest.TestCommand{ID: "test-123"}

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
		_ = m.Register(registry)

		middleware := m.CommandMiddleware()
		cmd := &minktest.TestCommand{ID: "test-123"}
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
		_ = m.Register(registry)

		middleware := m.CommandMiddleware()
		cmd := &minktest.TestCommand{ID: "test-123"}

		inFlightDuringExecution := float64(-1)

		handler := middleware(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			// Capture in-flight during execution
			inFlightDuringExecution = testutil.ToFloat64(m.commandsInFlight.WithLabelValues("test", "TestCommand"))
			return mink.NewSuccessResult("test-123", 1), nil
		})

		_, _ = handler(context.Background(), cmd)

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
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{}
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
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{AppendErr: errors.New("append failed")}
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
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{
			Events: []adapters.StoredEvent{
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
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{LoadErr: errors.New("load failed")}
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
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{
			Events: []adapters.StoredEvent{
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
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{
			Events: []adapters.StoredEvent{
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
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{}
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
		projection := &minktest.MockProjection{ProjectionName: "TestProjection"}
		middleware := m.WrapProjection(projection)

		assert.Equal(t, "TestProjection", middleware.Name())
	})

	t.Run("HandledEvents delegates to underlying projection", func(t *testing.T) {
		m := New()
		projection := &minktest.MockProjection{EventTypes: []string{"Event1", "Event2"}}
		middleware := m.WrapProjection(projection)

		assert.Equal(t, []string{"Event1", "Event2"}, middleware.HandledEvents())
	})

	t.Run("records successful apply", func(t *testing.T) {
		m := New(WithNamespace("proj_success"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

		projection := &minktest.MockProjection{ProjectionName: "OrderProjection"}
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
		_ = m.Register(registry)

		projection := &minktest.MockProjection{
			ProjectionName: "OrderProjection",
			ApplyErr:       errors.New("apply failed"),
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
		_ = m.Register(registry)

		m.RecordProjectionLag("OrderProjection", 50)

		lag := testutil.ToFloat64(m.projectionLag.WithLabelValues("test", "OrderProjection"))
		assert.Equal(t, float64(50), lag)
	})
}

func TestMetrics_RecordProjectionCheckpoint(t *testing.T) {
	t.Run("records checkpoint", func(t *testing.T) {
		m := New(WithNamespace("cp_test"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

		m.RecordProjectionCheckpoint("OrderProjection", 1000)

		checkpoint := testutil.ToFloat64(m.projectionCheckpoint.WithLabelValues("test", "OrderProjection"))
		assert.Equal(t, float64(1000), checkpoint)
	})
}

func TestMetrics_RecordError(t *testing.T) {
	t.Run("records custom error", func(t *testing.T) {
		m := New(WithNamespace("err_test"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

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

// =============================================================================
// Subscription Tests
// =============================================================================

func TestEventStoreMiddleware_SupportsSubscriptions(t *testing.T) {
	t.Run("returns true for subscription adapter", func(t *testing.T) {
		m := New()
		adapter := &minktest.MockAdapter{}
		middleware := m.WrapEventStore(adapter)

		// MockAdapter implements SubscriptionAdapter
		assert.True(t, middleware.SupportsSubscriptions())
	})
}

func TestEventStoreMiddleware_LoadFromPosition(t *testing.T) {
	t.Run("records successful load from position", func(t *testing.T) {
		m := New(WithNamespace("es_lfp_success"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{
			Events: []adapters.StoredEvent{
				{ID: "event-1", GlobalPosition: 10},
				{ID: "event-2", GlobalPosition: 11},
			},
		}
		middleware := m.WrapEventStore(adapter)

		events, err := middleware.LoadFromPosition(context.Background(), 0, 10)

		require.NoError(t, err)
		assert.Len(t, events, 2)

		// Verify metrics
		successCount := testutil.ToFloat64(m.eventStoreOperationsTotal.WithLabelValues("test", "load_from_position", StatusSuccess))
		assert.Equal(t, float64(1), successCount)
	})

	t.Run("records failed load from position", func(t *testing.T) {
		m := New(WithNamespace("es_lfp_fail"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{LoadFromPositionErr: errors.New("load failed")}
		middleware := m.WrapEventStore(adapter)

		_, err := middleware.LoadFromPosition(context.Background(), 0, 10)

		require.Error(t, err)

		// Verify error metrics
		errorCount := testutil.ToFloat64(m.errorsTotal.WithLabelValues("test", "load_from_position_error"))
		assert.Equal(t, float64(1), errorCount)
	})
}

func TestEventStoreMiddleware_SubscribeAll(t *testing.T) {
	t.Run("records successful subscribe all", func(t *testing.T) {
		m := New(WithNamespace("es_sub_all"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{}
		middleware := m.WrapEventStore(adapter)

		ch, err := middleware.SubscribeAll(context.Background(), 0)

		require.NoError(t, err)
		assert.NotNil(t, ch)

		// Verify metrics
		count := testutil.ToFloat64(m.eventStoreOperationsTotal.WithLabelValues("test", OperationSubscribe, StatusSuccess))
		assert.Equal(t, float64(1), count)
	})
}

func TestEventStoreMiddleware_SubscribeStream(t *testing.T) {
	t.Run("records successful subscribe stream", func(t *testing.T) {
		m := New(WithNamespace("es_sub_stream"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{}
		middleware := m.WrapEventStore(adapter)

		ch, err := middleware.SubscribeStream(context.Background(), "order-123", 0)

		require.NoError(t, err)
		assert.NotNil(t, ch)

		// Verify metrics
		count := testutil.ToFloat64(m.eventStoreOperationsTotal.WithLabelValues("test", OperationSubscribe, StatusSuccess))
		assert.Equal(t, float64(1), count)
	})
}

func TestEventStoreMiddleware_SubscribeCategory(t *testing.T) {
	t.Run("records successful subscribe category", func(t *testing.T) {
		m := New(WithNamespace("es_sub_cat"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{}
		middleware := m.WrapEventStore(adapter)

		ch, err := middleware.SubscribeCategory(context.Background(), "Order", 0)

		require.NoError(t, err)
		assert.NotNil(t, ch)

		// Verify metrics
		count := testutil.ToFloat64(m.eventStoreOperationsTotal.WithLabelValues("test", OperationSubscribe, StatusSuccess))
		assert.Equal(t, float64(1), count)
	})
}

func TestEventStoreMiddleware_Close(t *testing.T) {
	t.Run("closes underlying adapter", func(t *testing.T) {
		m := New()
		adapter := &minktest.MockAdapter{}
		middleware := m.WrapEventStore(adapter)

		err := middleware.Close()

		require.NoError(t, err)
	})
}

// =============================================================================
// Error Type Name Tests
// =============================================================================

func TestErrorTypeName(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "none",
		},
		{
			name:     "concurrency conflict",
			err:      mink.ErrConcurrencyConflict,
			expected: "concurrency_conflict",
		},
		{
			name:     "stream not found",
			err:      mink.ErrStreamNotFound,
			expected: "stream_not_found",
		},
		{
			name:     "handler not found",
			err:      mink.ErrHandlerNotFound,
			expected: "handler_not_found",
		},
		{
			name:     "validation failed",
			err:      mink.ErrValidationFailed,
			expected: "validation_failed",
		},
		{
			name:     "command already processed",
			err:      mink.ErrCommandAlreadyProcessed,
			expected: "command_already_processed",
		},
		{
			name:     "handler panicked",
			err:      mink.ErrHandlerPanicked,
			expected: "handler_panicked",
		},
		{
			name:     "serialization failed",
			err:      mink.ErrSerializationFailed,
			expected: "serialization_failed",
		},
		{
			name:     "event type not registered",
			err:      mink.ErrEventTypeNotRegistered,
			expected: "event_type_not_registered",
		},
		{
			name:     "nil aggregate",
			err:      mink.ErrNilAggregate,
			expected: "nil_aggregate",
		},
		{
			name:     "nil command",
			err:      mink.ErrNilCommand,
			expected: "nil_command",
		},
		{
			name:     "projection failed",
			err:      mink.ErrProjectionFailed,
			expected: "projection_failed",
		},
		{
			name:     "empty stream id",
			err:      adapters.ErrEmptyStreamID,
			expected: "empty_stream_id",
		},
		{
			name:     "no events",
			err:      adapters.ErrNoEvents,
			expected: "no_events",
		},
		{
			name:     "invalid version",
			err:      adapters.ErrInvalidVersion,
			expected: "invalid_version",
		},
		{
			name:     "adapter closed",
			err:      adapters.ErrAdapterClosed,
			expected: "adapter_closed",
		},
		{
			name:     "unknown error",
			err:      errors.New("some random error"),
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := errorTypeName(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// =============================================================================
// Command Result Error Recording Tests
// =============================================================================

func TestMetrics_CommandMiddleware_ErrorResult(t *testing.T) {
	t.Run("records error from result when no handler error", func(t *testing.T) {
		m := New(WithNamespace("cmd_result_err"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

		middleware := m.CommandMiddleware()
		cmd := &minktest.TestCommand{ID: "test-123"}
		resultErr := mink.ErrValidationFailed

		handler := middleware(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			return mink.NewErrorResult(resultErr), nil // Error in result, not handler
		})

		result, err := handler(context.Background(), cmd)

		require.NoError(t, err) // Handler didn't return error
		assert.True(t, result.IsError())

		// Verify error was recorded
		errorCount := testutil.ToFloat64(m.errorsTotal.WithLabelValues("test", "validation_failed"))
		assert.Equal(t, float64(1), errorCount)
	})
}

// =============================================================================
// GetStreamInfo Error Test
// =============================================================================

func TestEventStoreMiddleware_GetStreamInfo_Error(t *testing.T) {
	t.Run("records error for get stream info", func(t *testing.T) {
		m := New(WithNamespace("es_info_err"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{GetStreamInfoErr: errors.New("stream info error")}
		middleware := m.WrapEventStore(adapter)

		_, err := middleware.GetStreamInfo(context.Background(), "order-123")

		require.Error(t, err)

		// Verify error metrics
		errorCount := testutil.ToFloat64(m.eventStoreOperationsTotal.WithLabelValues("test", "get_stream_info", StatusError))
		assert.Equal(t, float64(1), errorCount)
	})
}

func TestEventStoreMiddleware_GetLastPosition_Error(t *testing.T) {
	t.Run("records error for get last position", func(t *testing.T) {
		m := New(WithNamespace("es_pos_err"), WithMetricsServiceName("test"))
		registry := prometheus.NewRegistry()
		_ = m.Register(registry)

		adapter := &minktest.MockAdapter{GetLastPositionErr: errors.New("position error")}
		middleware := m.WrapEventStore(adapter)

		_, err := middleware.GetLastPosition(context.Background())

		require.Error(t, err)

		// Verify error metrics
		errorCount := testutil.ToFloat64(m.eventStoreOperationsTotal.WithLabelValues("test", "get_last_position", StatusError))
		assert.Equal(t, float64(1), errorCount)
	})
}

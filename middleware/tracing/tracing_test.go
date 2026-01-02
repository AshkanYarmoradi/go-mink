package tracing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

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
			Metadata:       e.Metadata,
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
// Test Helpers
// =============================================================================

func setupTestTracer(t *testing.T) (*Tracer, *tracetest.InMemoryExporter) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		tp.Shutdown(context.Background())
	})

	tracer := NewTracer(WithTracerProvider(tp))
	return tracer, exporter
}

// =============================================================================
// Tracer Tests
// =============================================================================

func TestNewTracer(t *testing.T) {
	t.Run("creates tracer with defaults", func(t *testing.T) {
		tracer := NewTracer()

		assert.NotNil(t, tracer)
		assert.Equal(t, DefaultServiceName, tracer.ServiceName())
		assert.NotNil(t, tracer.Tracer())
	})

	t.Run("with custom service name", func(t *testing.T) {
		tracer := NewTracer(WithServiceName("custom-service"))

		assert.Equal(t, "custom-service", tracer.ServiceName())
	})

	t.Run("with custom tracer provider", func(t *testing.T) {
		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer tp.Shutdown(context.Background())

		tracer := NewTracer(WithTracerProvider(tp))

		assert.NotNil(t, tracer.Tracer())
	})
}

func TestTracer_StartSpan(t *testing.T) {
	t.Run("starts span", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)

		ctx, span := tracer.StartSpan(context.Background(), "test-span")
		span.End()

		assert.NotNil(t, ctx)
		assert.NotNil(t, span)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, "test-span", spans[0].Name)
	})
}

// =============================================================================
// Command Middleware Tests
// =============================================================================

func TestCommandMiddleware(t *testing.T) {
	t.Run("traces successful command", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		cmd := &testCommand{id: "test-123"}

		middleware := CommandMiddleware(tracer)
		handler := middleware(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			return mink.NewSuccessResult("test-123", 1), nil
		})

		result, err := handler(context.Background(), cmd)

		require.NoError(t, err)
		assert.True(t, result.IsSuccess())

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, "command.TestCommand", spans[0].Name)
		assert.Equal(t, codes.Ok, spans[0].Status.Code)

		// Check attributes
		attrs := spans[0].Attributes
		assertAttribute(t, attrs, "mink.command.type", "TestCommand")
		assertAttribute(t, attrs, "mink.command.aggregate_id", "test-123")
	})

	t.Run("traces failed command with error", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		cmd := &testCommand{id: "test-123"}
		expectedErr := errors.New("command failed")

		middleware := CommandMiddleware(tracer)
		handler := middleware(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			return mink.NewErrorResult(expectedErr), expectedErr
		})

		result, err := handler(context.Background(), cmd)

		require.Error(t, err)
		assert.False(t, result.IsSuccess())

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].Status.Code)
		require.Len(t, spans[0].Events, 1) // Error event recorded
	})

	t.Run("traces command with result error", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		cmd := &testCommand{id: "test-123"}
		resultErr := errors.New("result error")

		middleware := CommandMiddleware(tracer)
		handler := middleware(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			return mink.NewErrorResult(resultErr), nil
		})

		_, err := handler(context.Background(), cmd)

		require.NoError(t, err)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].Status.Code)
	})

	t.Run("includes correlation ID when present", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		cmd := &testCommand{id: "test-123"}

		ctx := context.WithValue(context.Background(), correlationIDContextKey{}, "correlation-123")

		middleware := CommandMiddleware(tracer)
		handler := middleware(func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			return mink.NewSuccessResult("test-123", 1), nil
		})

		_, _ = handler(ctx, cmd)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assertAttribute(t, spans[0].Attributes, "mink.correlation_id", "correlation-123")
	})
}

// =============================================================================
// Event Store Middleware Tests
// =============================================================================

func TestEventStoreMiddleware_Append(t *testing.T) {
	t.Run("traces successful append", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		adapter := &mockAdapter{}
		middleware := NewEventStoreMiddleware(adapter, tracer)

		events := []adapters.EventRecord{
			{Type: "OrderCreated", Data: []byte("{}")},
			{Type: "ItemAdded", Data: []byte("{}")},
		}

		stored, err := middleware.Append(context.Background(), "order-123", events, mink.NoStream)

		require.NoError(t, err)
		assert.Len(t, stored, 2)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, "eventstore.append", spans[0].Name)
		assert.Equal(t, codes.Ok, spans[0].Status.Code)

		attrs := spans[0].Attributes
		assertAttribute(t, attrs, "mink.stream_id", "order-123")
	})

	t.Run("traces failed append", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		adapter := &mockAdapter{appendErr: errors.New("append failed")}
		middleware := NewEventStoreMiddleware(adapter, tracer)

		_, err := middleware.Append(context.Background(), "order-123", []adapters.EventRecord{}, mink.NoStream)

		require.Error(t, err)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].Status.Code)
	})
}

func TestEventStoreMiddleware_Load(t *testing.T) {
	t.Run("traces successful load", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		adapter := &mockAdapter{
			events: []adapters.StoredEvent{
				{ID: "event-1", Type: "OrderCreated"},
				{ID: "event-2", Type: "ItemAdded"},
			},
		}
		middleware := NewEventStoreMiddleware(adapter, tracer)

		events, err := middleware.Load(context.Background(), "order-123", 0)

		require.NoError(t, err)
		assert.Len(t, events, 2)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, "eventstore.load", spans[0].Name)
		assert.Equal(t, codes.Ok, spans[0].Status.Code)
	})

	t.Run("traces failed load", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		adapter := &mockAdapter{loadErr: errors.New("load failed")}
		middleware := NewEventStoreMiddleware(adapter, tracer)

		_, err := middleware.Load(context.Background(), "order-123", 0)

		require.Error(t, err)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].Status.Code)
	})
}

func TestEventStoreMiddleware_GetStreamInfo(t *testing.T) {
	t.Run("traces successful get stream info", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		adapter := &mockAdapter{
			events: []adapters.StoredEvent{
				{ID: "event-1", Type: "OrderCreated", Version: 1},
				{ID: "event-2", Type: "ItemAdded", Version: 2},
			},
		}
		middleware := NewEventStoreMiddleware(adapter, tracer)

		info, err := middleware.GetStreamInfo(context.Background(), "order-123")

		require.NoError(t, err)
		assert.Equal(t, "order-123", info.StreamID)
		assert.Equal(t, int64(2), info.Version)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, "eventstore.get_stream_info", spans[0].Name)
		assert.Equal(t, codes.Ok, spans[0].Status.Code)
	})
}

func TestEventStoreMiddleware_GetLastPosition(t *testing.T) {
	t.Run("traces successful get last position", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		adapter := &mockAdapter{
			events: []adapters.StoredEvent{
				{ID: "event-1", GlobalPosition: 100},
			},
		}
		middleware := NewEventStoreMiddleware(adapter, tracer)

		pos, err := middleware.GetLastPosition(context.Background())

		require.NoError(t, err)
		assert.Equal(t, uint64(100), pos)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, "eventstore.get_last_position", spans[0].Name)
		assert.Equal(t, codes.Ok, spans[0].Status.Code)
	})
}

func TestEventStoreMiddleware_Initialize(t *testing.T) {
	t.Run("traces successful initialize", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		adapter := &mockAdapter{}
		middleware := NewEventStoreMiddleware(adapter, tracer)

		err := middleware.Initialize(context.Background())

		require.NoError(t, err)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, "eventstore.initialize", spans[0].Name)
		assert.Equal(t, codes.Ok, spans[0].Status.Code)
	})
}

// =============================================================================
// Projection Middleware Tests
// =============================================================================

func TestProjectionMiddleware(t *testing.T) {
	t.Run("Name delegates to underlying projection", func(t *testing.T) {
		tracer, _ := setupTestTracer(t)
		projection := &mockProjection{name: "TestProjection"}
		middleware := NewProjectionMiddleware(projection, tracer)

		assert.Equal(t, "TestProjection", middleware.Name())
	})

	t.Run("HandledEvents delegates to underlying projection", func(t *testing.T) {
		tracer, _ := setupTestTracer(t)
		projection := &mockProjection{events: []string{"Event1", "Event2"}}
		middleware := NewProjectionMiddleware(projection, tracer)

		assert.Equal(t, []string{"Event1", "Event2"}, middleware.HandledEvents())
	})

	t.Run("traces successful apply", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		projection := &mockProjection{name: "OrderProjection"}
		middleware := NewProjectionMiddleware(projection, tracer)

		event := mink.StoredEvent{
			ID:             "event-123",
			StreamID:       "order-123",
			Type:           "OrderCreated",
			Version:        1,
			GlobalPosition: 1,
		}

		err := middleware.Apply(context.Background(), event)

		require.NoError(t, err)
		require.Len(t, projection.applied, 1)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, "projection.OrderProjection.apply", spans[0].Name)
		assert.Equal(t, codes.Ok, spans[0].Status.Code)

		attrs := spans[0].Attributes
		assertAttribute(t, attrs, "mink.projection.name", "OrderProjection")
		assertAttribute(t, attrs, "mink.event.type", "OrderCreated")
	})

	t.Run("traces failed apply", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)
		projection := &mockProjection{
			name:     "OrderProjection",
			applyErr: errors.New("apply failed"),
		}
		middleware := NewProjectionMiddleware(projection, tracer)

		event := mink.StoredEvent{ID: "event-123", Type: "OrderCreated"}

		err := middleware.Apply(context.Background(), event)

		require.Error(t, err)

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].Status.Code)
	})
}

// =============================================================================
// Span Helper Tests
// =============================================================================

func TestSpanFromContext(t *testing.T) {
	t.Run("returns span from context", func(t *testing.T) {
		tracer, _ := setupTestTracer(t)

		ctx, span := tracer.StartSpan(context.Background(), "test")
		defer span.End()

		retrieved := SpanFromContext(ctx)
		assert.Equal(t, span, retrieved)
	})
}

func TestAddEvent(t *testing.T) {
	t.Run("adds event to span", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)

		ctx, span := tracer.StartSpan(context.Background(), "test")
		AddEvent(ctx, "test-event", trace.WithAttributes(
			attribute.String("key", "value"),
		))
		span.End()

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		require.Len(t, spans[0].Events, 1)
		assert.Equal(t, "test-event", spans[0].Events[0].Name)
	})
}

func TestSetError(t *testing.T) {
	t.Run("sets error on span", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)

		ctx, span := tracer.StartSpan(context.Background(), "test")
		SetError(ctx, errors.New("test error"))
		span.End()

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].Status.Code)
	})
}

func TestSetAttributes(t *testing.T) {
	t.Run("sets attributes on span", func(t *testing.T) {
		tracer, exporter := setupTestTracer(t)

		ctx, span := tracer.StartSpan(context.Background(), "test")
		SetAttributes(ctx, attribute.String("custom.key", "custom.value"))
		span.End()

		spans := exporter.GetSpans()
		require.Len(t, spans, 1)
		assertAttribute(t, spans[0].Attributes, "custom.key", "custom.value")
	})
}

// =============================================================================
// Test Helpers
// =============================================================================

func assertAttribute(t *testing.T, attrs []attribute.KeyValue, key string, expectedValue string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			assert.Equal(t, expectedValue, attr.Value.AsString(), "attribute %s has wrong value", key)
			return
		}
	}
	t.Errorf("attribute %s not found", key)
}

// Package tracing provides OpenTelemetry integration for mink.
//
// This package enables distributed tracing for event sourcing operations,
// including command execution, event store operations, and projections.
//
// Basic usage with command bus:
//
//	tp := sdktrace.NewTracerProvider(...)
//	otel.SetTracerProvider(tp)
//
//	tracer := tracing.NewTracer()
//	bus := mink.NewCommandBus()
//	bus.Use(tracing.CommandMiddleware(tracer))
//
// The tracing middleware captures:
//   - Command type and execution duration
//   - Success/failure status
//   - Error details when commands fail
//   - Correlation and causation IDs
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters"
)

const (
	// TracerName is the name of the mink tracer.
	TracerName = "github.com/AshkanYarmoradi/go-mink"

	// DefaultServiceName is the default service name for spans.
	DefaultServiceName = "mink"
)

// Tracer wraps OpenTelemetry tracer for mink operations.
type Tracer struct {
	tracer      trace.Tracer
	serviceName string
}

// TracerOption configures a Tracer.
type TracerOption func(*Tracer)

// WithTracerProvider sets a custom TracerProvider.
func WithTracerProvider(tp trace.TracerProvider) TracerOption {
	return func(t *Tracer) {
		t.tracer = tp.Tracer(TracerName)
	}
}

// WithServiceName sets the service name for spans.
func WithServiceName(name string) TracerOption {
	return func(t *Tracer) {
		t.serviceName = name
	}
}

// NewTracer creates a new Tracer with the global TracerProvider.
func NewTracer(opts ...TracerOption) *Tracer {
	t := &Tracer{
		tracer:      otel.Tracer(TracerName),
		serviceName: DefaultServiceName,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// StartSpan starts a new span with the given name.
func (t *Tracer) StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, name, opts...)
}

// Tracer returns the underlying OpenTelemetry tracer.
func (t *Tracer) Tracer() trace.Tracer {
	return t.tracer
}

// ServiceName returns the configured service name.
func (t *Tracer) ServiceName() string {
	return t.serviceName
}

// =============================================================================
// Command Middleware
// =============================================================================

// CommandMiddleware creates middleware that traces command execution.
func CommandMiddleware(tracer *Tracer) mink.Middleware {
	return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
		return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			spanName := fmt.Sprintf("command.%s", cmd.CommandType())

			ctx, span := tracer.StartSpan(ctx, spanName,
				trace.WithSpanKind(trace.SpanKindInternal),
			)
			defer span.End()

			// Set command attributes
			attrs := []attribute.KeyValue{
				attribute.String("mink.service", tracer.serviceName),
				attribute.String("mink.command.type", cmd.CommandType()),
			}

			// Check if command implements AggregateCommand
			if aggCmd, ok := cmd.(mink.AggregateCommand); ok {
				attrs = append(attrs, attribute.String("mink.command.aggregate_id", aggCmd.AggregateID()))
			}

			span.SetAttributes(attrs...)

			// Extract correlation ID if present
			if correlationID, ok := ctx.Value(correlationIDContextKey{}).(string); ok {
				span.SetAttributes(attribute.String("mink.correlation_id", correlationID))
			}

			// Execute command
			result, err := next(ctx, cmd)

			// Record result
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			} else if result.IsError() {
				span.RecordError(result.Error)
				span.SetStatus(codes.Error, result.Error.Error())
			} else {
				span.SetStatus(codes.Ok, "")
				span.SetAttributes(
					attribute.String("mink.result.aggregate_id", result.AggregateID),
					attribute.Int64("mink.result.version", result.Version),
				)
			}

			return result, err
		}
	}
}

// correlationIDContextKey is a context key for correlation ID.
type correlationIDContextKey struct{}

// =============================================================================
// Event Store Middleware
// =============================================================================

// EventStoreMiddleware wraps an EventStoreAdapter with tracing.
type EventStoreMiddleware struct {
	adapter adapters.EventStoreAdapter
	tracer  *Tracer
}

// NewEventStoreMiddleware wraps an adapter with tracing.
func NewEventStoreMiddleware(adapter adapters.EventStoreAdapter, tracer *Tracer) *EventStoreMiddleware {
	return &EventStoreMiddleware{
		adapter: adapter,
		tracer:  tracer,
	}
}

// Append stores events with tracing.
func (m *EventStoreMiddleware) Append(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
	ctx, span := m.tracer.StartSpan(ctx, "eventstore.append",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	defer span.End()

	span.SetAttributes(
		attribute.String("mink.service", m.tracer.serviceName),
		attribute.String("mink.stream_id", streamID),
		attribute.Int64("mink.expected_version", expectedVersion),
		attribute.Int("mink.events.count", len(events)),
	)

	if len(events) > 0 {
		eventTypes := make([]string, len(events))
		for i, e := range events {
			eventTypes[i] = e.Type
		}
		span.SetAttributes(attribute.StringSlice("mink.events.types", eventTypes))
	}

	stored, err := m.adapter.Append(ctx, streamID, events, expectedVersion)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
		if len(stored) > 0 {
			span.SetAttributes(
				attribute.Int64("mink.stored.version", stored[len(stored)-1].Version),
				attribute.Int64("mink.stored.global_position", int64(stored[len(stored)-1].GlobalPosition)),
			)
		}
	}

	return stored, err
}

// Load retrieves events with tracing.
func (m *EventStoreMiddleware) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	ctx, span := m.tracer.StartSpan(ctx, "eventstore.load",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	defer span.End()

	span.SetAttributes(
		attribute.String("mink.service", m.tracer.serviceName),
		attribute.String("mink.stream_id", streamID),
		attribute.Int64("mink.from_version", fromVersion),
	)

	events, err := m.adapter.Load(ctx, streamID, fromVersion)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
		span.SetAttributes(attribute.Int("mink.events.loaded", len(events)))
	}

	return events, err
}

// GetStreamInfo returns stream metadata with tracing.
func (m *EventStoreMiddleware) GetStreamInfo(ctx context.Context, streamID string) (*adapters.StreamInfo, error) {
	ctx, span := m.tracer.StartSpan(ctx, "eventstore.get_stream_info",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	defer span.End()

	span.SetAttributes(
		attribute.String("mink.service", m.tracer.serviceName),
		attribute.String("mink.stream_id", streamID),
	)

	info, err := m.adapter.GetStreamInfo(ctx, streamID)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
		span.SetAttributes(attribute.Int64("mink.stream.version", info.Version))
	}

	return info, err
}

// GetLastPosition returns the last global position with tracing.
func (m *EventStoreMiddleware) GetLastPosition(ctx context.Context) (uint64, error) {
	ctx, span := m.tracer.StartSpan(ctx, "eventstore.get_last_position",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	defer span.End()

	span.SetAttributes(attribute.String("mink.service", m.tracer.serviceName))

	pos, err := m.adapter.GetLastPosition(ctx)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
		span.SetAttributes(attribute.Int64("mink.last_position", int64(pos)))
	}

	return pos, err
}

// Initialize initializes the adapter with tracing.
func (m *EventStoreMiddleware) Initialize(ctx context.Context) error {
	ctx, span := m.tracer.StartSpan(ctx, "eventstore.initialize",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	defer span.End()

	span.SetAttributes(attribute.String("mink.service", m.tracer.serviceName))

	err := m.adapter.Initialize(ctx)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return err
}

// Close closes the adapter with tracing.
func (m *EventStoreMiddleware) Close() error {
	return m.adapter.Close()
}

// =============================================================================
// Projection Middleware
// =============================================================================

// ProjectionMiddleware wraps an inline projection with tracing.
type ProjectionMiddleware struct {
	projection mink.InlineProjection
	tracer     *Tracer
}

// NewProjectionMiddleware wraps an inline projection with tracing.
func NewProjectionMiddleware(projection mink.InlineProjection, tracer *Tracer) *ProjectionMiddleware {
	return &ProjectionMiddleware{
		projection: projection,
		tracer:     tracer,
	}
}

// Name returns the projection name.
func (m *ProjectionMiddleware) Name() string {
	return m.projection.Name()
}

// HandledEvents returns the handled event types.
func (m *ProjectionMiddleware) HandledEvents() []string {
	return m.projection.HandledEvents()
}

// Apply applies an event with tracing.
func (m *ProjectionMiddleware) Apply(ctx context.Context, event mink.StoredEvent) error {
	spanName := fmt.Sprintf("projection.%s.apply", m.projection.Name())

	ctx, span := m.tracer.StartSpan(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	span.SetAttributes(
		attribute.String("mink.service", m.tracer.serviceName),
		attribute.String("mink.projection.name", m.projection.Name()),
		attribute.String("mink.event.type", event.Type),
		attribute.String("mink.event.id", event.ID),
		attribute.String("mink.event.stream_id", event.StreamID),
		attribute.Int64("mink.event.version", event.Version),
		attribute.Int64("mink.event.global_position", int64(event.GlobalPosition)),
	)

	err := m.projection.Apply(ctx, event)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return err
}

// =============================================================================
// Span Helpers
// =============================================================================

// SpanFromContext returns the current span from context.
func SpanFromContext(ctx context.Context) trace.Span {
	return trace.SpanFromContext(ctx)
}

// AddEvent adds an event to the current span.
func AddEvent(ctx context.Context, name string, opts ...trace.EventOption) {
	span := trace.SpanFromContext(ctx)
	span.AddEvent(name, opts...)
}

// SetError sets an error on the current span.
func SetError(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// SetAttributes sets attributes on the current span.
func SetAttributes(ctx context.Context, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attrs...)
}

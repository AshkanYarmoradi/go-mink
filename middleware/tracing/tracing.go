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
//
// # Error details and PII
//
// By default the middlewares copy the full error text of a failed operation
// into the span status description and into the recorded exception event.
// Error messages frequently embed request data (validation errors echo field
// values, storage errors echo stream ids, wrapped errors echo whatever the
// handler formatted), so a trace backend can end up holding personal data.
// Use WithErrorRedaction to map errors to a safe description, or
// WithoutErrorDetails to replace every error with a fixed generic message.
//
// # Attribute length
//
// Span attributes built from runtime values (command types, aggregate and
// stream ids, correlation ids, event types and ids, projection names) are
// capped at DefaultMaxAttributeLength runes, because several of those values
// are client-supplied and would otherwise be recorded unbounded. The cap is
// configurable with WithMaxAttributeLength.
package tracing

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"go-mink.dev"
	"go-mink.dev/adapters"
)

const (
	// TracerName is the name of the mink tracer.
	TracerName = "go-mink.dev"

	// DefaultServiceName is the default service name for spans.
	DefaultServiceName = "mink"

	// DefaultMaxAttributeLength is the default cap, in runes, applied to every
	// span attribute that is built from a runtime value. Longer values are
	// truncated. See WithMaxAttributeLength.
	DefaultMaxAttributeLength = 256

	// RedactedErrorMessage is the generic description that WithoutErrorDetails
	// records in place of the real error text.
	RedactedErrorMessage = "operation failed"
)

// Tracer wraps OpenTelemetry tracer for mink operations.
type Tracer struct {
	tracer      trace.Tracer
	serviceName string

	// redactError, when non-nil, maps an operation error to the text that is
	// recorded on the span. nil records the raw error (the default).
	redactError func(error) string

	// maxAttrLen caps runtime-derived string attributes in runes. A value <= 0
	// disables the cap.
	maxAttrLen int
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

// WithErrorRedaction sets a function that decides what text is recorded on a
// span when an operation fails. The middlewares then call span.SetStatus with
// fn(err) as the description and record errors.New(fn(err)) as the exception
// event instead of the raw error, so the original error text (which may embed
// personal data) never reaches the trace backend. The error returned to the
// caller is not affected.
//
// The redaction applies to every span this package creates: command spans,
// event store spans and projection spans. Passing nil restores the default
// behavior of recording the raw error.
func WithErrorRedaction(fn func(error) string) TracerOption {
	return func(t *Tracer) {
		t.redactError = fn
	}
}

// WithoutErrorDetails records the fixed RedactedErrorMessage for every failed
// operation instead of the error text. It is shorthand for
// WithErrorRedaction(func(error) string { return RedactedErrorMessage }).
func WithoutErrorDetails() TracerOption {
	return WithErrorRedaction(func(error) string { return RedactedErrorMessage })
}

// WithMaxAttributeLength caps, in runes, every span attribute that is built
// from a runtime value (command type, aggregate id, stream id, correlation id,
// event type, event id, projection name). Values longer than n are truncated.
// The default is DefaultMaxAttributeLength. A value of zero or less disables
// the cap and records the values unbounded.
func WithMaxAttributeLength(n int) TracerOption {
	return func(t *Tracer) {
		t.maxAttrLen = n
	}
}

// NewTracer creates a new Tracer with the global TracerProvider.
func NewTracer(opts ...TracerOption) *Tracer {
	t := &Tracer{
		tracer:      otel.Tracer(TracerName),
		serviceName: DefaultServiceName,
		maxAttrLen:  DefaultMaxAttributeLength,
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

// MaxAttributeLength returns the configured attribute cap in runes. A value of
// zero or less means the cap is disabled.
func (t *Tracer) MaxAttributeLength() int {
	return t.maxAttrLen
}

// recordError marks span as failed. With no redaction configured it records
// the raw error; otherwise it records only the redacted description.
func (t *Tracer) recordError(span trace.Span, err error) {
	if t.redactError == nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
	msg := t.redactError(err)
	span.RecordError(errors.New(msg))
	span.SetStatus(codes.Error, msg)
}

// attr caps a runtime-derived attribute value at the configured length.
func (t *Tracer) attr(s string) string {
	return truncate(s, t.maxAttrLen)
}

// truncate returns s cut to at most maxRunes runes. A maxRunes of zero or
// less returns s unchanged. The cut happens on a rune boundary so the result
// is always valid UTF-8 when s is.
func truncate(s string, maxRunes int) string {
	if maxRunes <= 0 || len(s) <= maxRunes {
		// A byte length within the cap implies a rune count within the cap.
		return s
	}
	n := 0
	for i := range s {
		if n == maxRunes {
			return s[:i]
		}
		n++
	}
	return s
}

// =============================================================================
// Command Middleware
// =============================================================================

// CommandMiddleware creates middleware that traces command execution.
//
// Failed commands are recorded on the span according to the tracer's error
// redaction setting (see WithErrorRedaction); by default the raw error text is
// recorded. String attributes are capped per WithMaxAttributeLength.
func CommandMiddleware(tracer *Tracer) mink.Middleware {
	return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
		return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			commandType := tracer.attr(cmd.CommandType())
			spanName := fmt.Sprintf("command.%s", commandType)

			ctx, span := tracer.StartSpan(ctx, spanName,
				trace.WithSpanKind(trace.SpanKindInternal),
			)
			defer span.End()

			// Set command attributes
			attrs := []attribute.KeyValue{
				attribute.String("mink.service", tracer.serviceName),
				attribute.String("mink.command.type", commandType),
			}

			// Check if command implements AggregateCommand
			if aggCmd, ok := cmd.(mink.AggregateCommand); ok {
				attrs = append(attrs, attribute.String("mink.command.aggregate_id", tracer.attr(aggCmd.AggregateID())))
			}

			span.SetAttributes(attrs...)

			// Extract correlation ID if present. This must read the same context
			// key that mink.CorrelationIDMiddleware writes — use the public
			// accessor rather than a private key local to this package.
			if correlationID := mink.CorrelationIDFromContext(ctx); correlationID != "" {
				span.SetAttributes(attribute.String("mink.correlation_id", tracer.attr(correlationID)))
			}

			// Execute command
			result, err := next(ctx, cmd)

			// Record result
			if err != nil {
				tracer.recordError(span, err)
			} else if result.IsError() {
				tracer.recordError(span, result.Error)
			} else {
				span.SetStatus(codes.Ok, "")
				span.SetAttributes(
					attribute.String("mink.result.aggregate_id", tracer.attr(result.AggregateID)),
					attribute.Int64("mink.result.version", result.Version),
				)
			}

			return result, err
		}
	}
}

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
		attribute.String("mink.stream_id", m.tracer.attr(streamID)),
		attribute.Int64("mink.expected_version", expectedVersion),
		attribute.Int("mink.events.count", len(events)),
	)

	if len(events) > 0 {
		eventTypes := make([]string, len(events))
		for i, e := range events {
			eventTypes[i] = m.tracer.attr(e.Type)
		}
		span.SetAttributes(attribute.StringSlice("mink.events.types", eventTypes))
	}

	stored, err := m.adapter.Append(ctx, streamID, events, expectedVersion)

	if err != nil {
		m.tracer.recordError(span, err)
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
		attribute.String("mink.stream_id", m.tracer.attr(streamID)),
		attribute.Int64("mink.from_version", fromVersion),
	)

	events, err := m.adapter.Load(ctx, streamID, fromVersion)

	if err != nil {
		m.tracer.recordError(span, err)
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
		attribute.String("mink.stream_id", m.tracer.attr(streamID)),
	)

	info, err := m.adapter.GetStreamInfo(ctx, streamID)

	if err != nil {
		m.tracer.recordError(span, err)
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
		m.tracer.recordError(span, err)
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
		m.tracer.recordError(span, err)
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
	projectionName := m.tracer.attr(m.projection.Name())
	spanName := fmt.Sprintf("projection.%s.apply", projectionName)

	ctx, span := m.tracer.StartSpan(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	span.SetAttributes(
		attribute.String("mink.service", m.tracer.serviceName),
		attribute.String("mink.projection.name", projectionName),
		attribute.String("mink.event.type", m.tracer.attr(event.Type)),
		attribute.String("mink.event.id", m.tracer.attr(event.ID)),
		attribute.String("mink.event.stream_id", m.tracer.attr(event.StreamID)),
		attribute.Int64("mink.event.version", event.Version),
		attribute.Int64("mink.event.global_position", int64(event.GlobalPosition)),
	)

	err := m.projection.Apply(ctx, event)

	if err != nil {
		m.tracer.recordError(span, err)
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
//
// SetError is not bound to a Tracer, so it always records the raw error text;
// the WithErrorRedaction setting does not apply. Redact err yourself before
// calling it when the message may carry personal data.
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

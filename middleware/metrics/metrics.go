// Package metrics provides Prometheus metrics integration for mink.
//
// This package enables observability through Prometheus metrics for
// event sourcing operations, including command execution, event store
// operations, and projections.
//
// Basic usage:
//
//	metrics := metrics.New()
//	// Register with Prometheus
//	prometheus.MustRegister(metrics.Collectors()...)
//
//	// Use with command bus
//	bus := mink.NewCommandBus()
//	bus.Use(metrics.CommandMiddleware())
//
//	// Use with event store
//	tracedStore := metrics.WrapEventStore(adapter)
//
// The metrics collected include:
//   - Command execution counts and durations
//   - Event store operations (append, load, subscribe)
//   - Projection processing metrics
//   - Error counts by type
package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Default metric labels.
const (
	LabelCommandType    = "command_type"
	LabelAggregateType  = "aggregate_type"
	LabelEventType      = "event_type"
	LabelProjectionName = "projection_name"
	LabelOperation      = "operation"
	LabelStatus         = "status"
	LabelErrorType      = "error_type"
	LabelStreamID       = "stream_id"
	LabelService        = "service"
)

// Status values.
const (
	StatusSuccess = "success"
	StatusError   = "error"
)

// Operation values.
const (
	OperationAppend    = "append"
	OperationLoad      = "load"
	OperationSubscribe = "subscribe"
)

// Metrics holds all Prometheus metrics for mink.
type Metrics struct {
	namespace   string
	subsystem   string
	serviceName string

	// Command metrics
	commandsTotal    *prometheus.CounterVec
	commandDuration  *prometheus.HistogramVec
	commandsInFlight *prometheus.GaugeVec

	// Event store metrics
	eventStoreOperationsTotal   *prometheus.CounterVec
	eventStoreOperationDuration *prometheus.HistogramVec
	eventsAppendedTotal         *prometheus.CounterVec
	eventsLoadedTotal           *prometheus.CounterVec

	// Projection metrics
	projectionsProcessedTotal *prometheus.CounterVec
	projectionDuration        *prometheus.HistogramVec
	projectionLag             *prometheus.GaugeVec
	projectionCheckpoint      *prometheus.GaugeVec

	// Error metrics
	errorsTotal *prometheus.CounterVec
}

// MetricsOption configures Metrics.
type MetricsOption func(*Metrics)

// WithNamespace sets the Prometheus namespace.
func WithNamespace(namespace string) MetricsOption {
	return func(m *Metrics) {
		m.namespace = namespace
	}
}

// WithSubsystem sets the Prometheus subsystem.
func WithSubsystem(subsystem string) MetricsOption {
	return func(m *Metrics) {
		m.subsystem = subsystem
	}
}

// WithMetricsServiceName sets the service name label.
func WithMetricsServiceName(name string) MetricsOption {
	return func(m *Metrics) {
		m.serviceName = name
	}
}

// New creates a new Metrics instance with default settings.
func New(opts ...MetricsOption) *Metrics {
	m := &Metrics{
		namespace:   "mink",
		subsystem:   "",
		serviceName: "unknown",
	}

	for _, opt := range opts {
		opt(m)
	}

	m.initMetrics()
	return m
}

// initMetrics initializes all Prometheus metrics.
func (m *Metrics) initMetrics() {
	// Command metrics
	m.commandsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "commands_total",
			Help:      "Total number of commands processed.",
		},
		[]string{LabelService, LabelCommandType, LabelStatus},
	)

	m.commandDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "command_duration_seconds",
			Help:      "Duration of command processing in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{LabelService, LabelCommandType},
	)

	m.commandsInFlight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "commands_in_flight",
			Help:      "Number of commands currently being processed.",
		},
		[]string{LabelService, LabelCommandType},
	)

	// Event store metrics
	m.eventStoreOperationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "eventstore_operations_total",
			Help:      "Total number of event store operations.",
		},
		[]string{LabelService, LabelOperation, LabelStatus},
	)

	m.eventStoreOperationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "eventstore_operation_duration_seconds",
			Help:      "Duration of event store operations in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{LabelService, LabelOperation},
	)

	m.eventsAppendedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "events_appended_total",
			Help:      "Total number of events appended to streams.",
		},
		[]string{LabelService, LabelEventType},
	)

	m.eventsLoadedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "events_loaded_total",
			Help:      "Total number of events loaded from streams.",
		},
		[]string{LabelService},
	)

	// Projection metrics
	m.projectionsProcessedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "projections_processed_total",
			Help:      "Total number of events processed by projections.",
		},
		[]string{LabelService, LabelProjectionName, LabelEventType, LabelStatus},
	)

	m.projectionDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "projection_duration_seconds",
			Help:      "Duration of projection event processing in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{LabelService, LabelProjectionName},
	)

	m.projectionLag = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "projection_lag_events",
			Help:      "Number of events behind the latest position for each projection.",
		},
		[]string{LabelService, LabelProjectionName},
	)

	m.projectionCheckpoint = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "projection_checkpoint_position",
			Help:      "Current checkpoint position for each projection.",
		},
		[]string{LabelService, LabelProjectionName},
	)

	// Error metrics
	m.errorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: m.namespace,
			Subsystem: m.subsystem,
			Name:      "errors_total",
			Help:      "Total number of errors by type.",
		},
		[]string{LabelService, LabelErrorType},
	)
}

// Collectors returns all Prometheus collectors for registration.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.commandsTotal,
		m.commandDuration,
		m.commandsInFlight,
		m.eventStoreOperationsTotal,
		m.eventStoreOperationDuration,
		m.eventsAppendedTotal,
		m.eventsLoadedTotal,
		m.projectionsProcessedTotal,
		m.projectionDuration,
		m.projectionLag,
		m.projectionCheckpoint,
		m.errorsTotal,
	}
}

// MustRegister registers all collectors with the default registry.
// Panics if registration fails.
func (m *Metrics) MustRegister() {
	prometheus.MustRegister(m.Collectors()...)
}

// Register registers all collectors with the given registry.
func (m *Metrics) Register(registry prometheus.Registerer) error {
	for _, collector := range m.Collectors() {
		if err := registry.Register(collector); err != nil {
			return err
		}
	}
	return nil
}

// =============================================================================
// Command Middleware
// =============================================================================

// CommandMiddleware returns middleware that records command metrics.
func (m *Metrics) CommandMiddleware() mink.Middleware {
	return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
		return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			cmdType := cmd.CommandType()

			// Track in-flight
			m.commandsInFlight.WithLabelValues(m.serviceName, cmdType).Inc()
			defer m.commandsInFlight.WithLabelValues(m.serviceName, cmdType).Dec()

			// Time execution
			start := time.Now()
			result, err := next(ctx, cmd)
			duration := time.Since(start)

			// Record metrics
			m.commandDuration.WithLabelValues(m.serviceName, cmdType).Observe(duration.Seconds())

			status := StatusSuccess
			if err != nil || result.IsError() {
				status = StatusError
				m.recordError(err, result)
			}

			m.commandsTotal.WithLabelValues(m.serviceName, cmdType, status).Inc()

			return result, err
		}
	}
}

// recordError records an error metric.
func (m *Metrics) recordError(err error, result mink.CommandResult) {
	errorType := "unknown"
	if err != nil {
		errorType = errorTypeName(err)
	} else if result.Error != nil {
		errorType = errorTypeName(result.Error)
	}
	m.errorsTotal.WithLabelValues(m.serviceName, errorType).Inc()
}

// errorTypeName extracts the error type name based on sentinel errors.
func errorTypeName(err error) string {
	if err == nil {
		return "none"
	}

	// Check for known mink error types
	switch {
	case errors.Is(err, mink.ErrConcurrencyConflict):
		return "concurrency_conflict"
	case errors.Is(err, mink.ErrStreamNotFound):
		return "stream_not_found"
	case errors.Is(err, mink.ErrHandlerNotFound):
		return "handler_not_found"
	case errors.Is(err, mink.ErrValidationFailed):
		return "validation_failed"
	case errors.Is(err, mink.ErrCommandAlreadyProcessed):
		return "command_already_processed"
	case errors.Is(err, mink.ErrHandlerPanicked):
		return "handler_panicked"
	case errors.Is(err, mink.ErrSerializationFailed):
		return "serialization_failed"
	case errors.Is(err, mink.ErrEventTypeNotRegistered):
		return "event_type_not_registered"
	case errors.Is(err, mink.ErrNilAggregate):
		return "nil_aggregate"
	case errors.Is(err, mink.ErrNilCommand):
		return "nil_command"
	case errors.Is(err, mink.ErrProjectionFailed):
		return "projection_failed"
	case errors.Is(err, adapters.ErrEmptyStreamID):
		return "empty_stream_id"
	case errors.Is(err, adapters.ErrNoEvents):
		return "no_events"
	case errors.Is(err, adapters.ErrInvalidVersion):
		return "invalid_version"
	case errors.Is(err, adapters.ErrAdapterClosed):
		return "adapter_closed"
	default:
		return "unknown"
	}
}

// =============================================================================
// Event Store Middleware
// =============================================================================

// EventStoreMiddleware wraps an EventStoreAdapter with metrics.
type EventStoreMiddleware struct {
	adapter adapters.EventStoreAdapter
	metrics *Metrics
}

// WrapEventStore wraps an adapter with metrics collection.
func (m *Metrics) WrapEventStore(adapter adapters.EventStoreAdapter) *EventStoreMiddleware {
	return &EventStoreMiddleware{
		adapter: adapter,
		metrics: m,
	}
}

// Append stores events with metrics.
func (em *EventStoreMiddleware) Append(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
	start := time.Now()
	stored, err := em.adapter.Append(ctx, streamID, events, expectedVersion)
	duration := time.Since(start)

	em.metrics.eventStoreOperationDuration.WithLabelValues(em.metrics.serviceName, OperationAppend).Observe(duration.Seconds())

	status := StatusSuccess
	if err != nil {
		status = StatusError
		em.metrics.errorsTotal.WithLabelValues(em.metrics.serviceName, "append_error").Inc()
	} else {
		// Count events by type
		for _, e := range events {
			em.metrics.eventsAppendedTotal.WithLabelValues(em.metrics.serviceName, e.Type).Inc()
		}
	}

	em.metrics.eventStoreOperationsTotal.WithLabelValues(em.metrics.serviceName, OperationAppend, status).Inc()

	return stored, err
}

// Load retrieves events with metrics.
func (em *EventStoreMiddleware) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	start := time.Now()
	events, err := em.adapter.Load(ctx, streamID, fromVersion)
	duration := time.Since(start)

	em.metrics.eventStoreOperationDuration.WithLabelValues(em.metrics.serviceName, OperationLoad).Observe(duration.Seconds())

	status := StatusSuccess
	if err != nil {
		status = StatusError
		em.metrics.errorsTotal.WithLabelValues(em.metrics.serviceName, "load_error").Inc()
	} else {
		em.metrics.eventsLoadedTotal.WithLabelValues(em.metrics.serviceName).Add(float64(len(events)))
	}

	em.metrics.eventStoreOperationsTotal.WithLabelValues(em.metrics.serviceName, OperationLoad, status).Inc()

	return events, err
}

// GetStreamInfo returns stream metadata with metrics.
func (em *EventStoreMiddleware) GetStreamInfo(ctx context.Context, streamID string) (*adapters.StreamInfo, error) {
	start := time.Now()
	info, err := em.adapter.GetStreamInfo(ctx, streamID)
	duration := time.Since(start)

	em.metrics.eventStoreOperationDuration.WithLabelValues(em.metrics.serviceName, "get_stream_info").Observe(duration.Seconds())

	status := StatusSuccess
	if err != nil {
		status = StatusError
	}

	em.metrics.eventStoreOperationsTotal.WithLabelValues(em.metrics.serviceName, "get_stream_info", status).Inc()

	return info, err
}

// GetLastPosition returns the last global position with metrics.
func (em *EventStoreMiddleware) GetLastPosition(ctx context.Context) (uint64, error) {
	start := time.Now()
	pos, err := em.adapter.GetLastPosition(ctx)
	duration := time.Since(start)

	em.metrics.eventStoreOperationDuration.WithLabelValues(em.metrics.serviceName, "get_last_position").Observe(duration.Seconds())

	status := StatusSuccess
	if err != nil {
		status = StatusError
	}

	em.metrics.eventStoreOperationsTotal.WithLabelValues(em.metrics.serviceName, "get_last_position", status).Inc()

	return pos, err
}

// Initialize initializes the adapter with metrics.
func (em *EventStoreMiddleware) Initialize(ctx context.Context) error {
	return em.adapter.Initialize(ctx)
}

// Close closes the adapter.
func (em *EventStoreMiddleware) Close() error {
	return em.adapter.Close()
}

// =============================================================================
// SubscriptionAdapter Support
// =============================================================================

// SupportsSubscriptions returns true if the underlying adapter supports subscriptions.
func (em *EventStoreMiddleware) SupportsSubscriptions() bool {
	_, ok := em.adapter.(adapters.SubscriptionAdapter)
	return ok
}

// LoadFromPosition loads events from a global position with metrics.
// Returns an error if the underlying adapter doesn't support subscriptions.
func (em *EventStoreMiddleware) LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]adapters.StoredEvent, error) {
	subAdapter, ok := em.adapter.(adapters.SubscriptionAdapter)
	if !ok {
		return nil, mink.ErrSubscriptionNotSupported
	}

	start := time.Now()
	events, err := subAdapter.LoadFromPosition(ctx, fromPosition, limit)
	duration := time.Since(start)

	em.metrics.eventStoreOperationDuration.WithLabelValues(em.metrics.serviceName, "load_from_position").Observe(duration.Seconds())

	status := StatusSuccess
	if err != nil {
		status = StatusError
		em.metrics.errorsTotal.WithLabelValues(em.metrics.serviceName, "load_from_position_error").Inc()
	} else {
		em.metrics.eventsLoadedTotal.WithLabelValues(em.metrics.serviceName).Add(float64(len(events)))
	}

	em.metrics.eventStoreOperationsTotal.WithLabelValues(em.metrics.serviceName, "load_from_position", status).Inc()

	return events, err
}

// SubscribeAll subscribes to all events with metrics.
// Returns an error if the underlying adapter doesn't support subscriptions.
func (em *EventStoreMiddleware) SubscribeAll(ctx context.Context, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	subAdapter, ok := em.adapter.(adapters.SubscriptionAdapter)
	if !ok {
		return nil, mink.ErrSubscriptionNotSupported
	}

	em.metrics.eventStoreOperationsTotal.WithLabelValues(em.metrics.serviceName, OperationSubscribe, StatusSuccess).Inc()

	return subAdapter.SubscribeAll(ctx, fromPosition, opts...)
}

// SubscribeStream subscribes to a stream with metrics.
// Returns an error if the underlying adapter doesn't support subscriptions.
func (em *EventStoreMiddleware) SubscribeStream(ctx context.Context, streamID string, fromVersion int64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	subAdapter, ok := em.adapter.(adapters.SubscriptionAdapter)
	if !ok {
		return nil, mink.ErrSubscriptionNotSupported
	}

	em.metrics.eventStoreOperationsTotal.WithLabelValues(em.metrics.serviceName, OperationSubscribe, StatusSuccess).Inc()

	return subAdapter.SubscribeStream(ctx, streamID, fromVersion, opts...)
}

// SubscribeCategory subscribes to a category with metrics.
// Returns an error if the underlying adapter doesn't support subscriptions.
func (em *EventStoreMiddleware) SubscribeCategory(ctx context.Context, category string, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	subAdapter, ok := em.adapter.(adapters.SubscriptionAdapter)
	if !ok {
		return nil, mink.ErrSubscriptionNotSupported
	}

	em.metrics.eventStoreOperationsTotal.WithLabelValues(em.metrics.serviceName, OperationSubscribe, StatusSuccess).Inc()

	return subAdapter.SubscribeCategory(ctx, category, fromPosition, opts...)
}

// =============================================================================
// Projection Middleware
// =============================================================================

// ProjectionMiddleware wraps an inline projection with metrics.
type ProjectionMiddleware struct {
	projection mink.InlineProjection
	metrics    *Metrics
}

// WrapProjection wraps an inline projection with metrics collection.
func (m *Metrics) WrapProjection(projection mink.InlineProjection) *ProjectionMiddleware {
	return &ProjectionMiddleware{
		projection: projection,
		metrics:    m,
	}
}

// Name returns the projection name.
func (pm *ProjectionMiddleware) Name() string {
	return pm.projection.Name()
}

// HandledEvents returns the handled event types.
func (pm *ProjectionMiddleware) HandledEvents() []string {
	return pm.projection.HandledEvents()
}

// Apply applies an event with metrics.
func (pm *ProjectionMiddleware) Apply(ctx context.Context, event mink.StoredEvent) error {
	projName := pm.projection.Name()

	start := time.Now()
	err := pm.projection.Apply(ctx, event)
	duration := time.Since(start)

	pm.metrics.projectionDuration.WithLabelValues(pm.metrics.serviceName, projName).Observe(duration.Seconds())

	status := StatusSuccess
	if err != nil {
		status = StatusError
		pm.metrics.errorsTotal.WithLabelValues(pm.metrics.serviceName, "projection_error").Inc()
	}

	pm.metrics.projectionsProcessedTotal.WithLabelValues(pm.metrics.serviceName, projName, event.Type, status).Inc()

	// Update checkpoint
	pm.metrics.projectionCheckpoint.WithLabelValues(pm.metrics.serviceName, projName).Set(float64(event.GlobalPosition))

	return err
}

// =============================================================================
// Manual Metric Recording
// =============================================================================

// RecordProjectionLag records the current lag for a projection.
func (m *Metrics) RecordProjectionLag(projectionName string, lag int64) {
	m.projectionLag.WithLabelValues(m.serviceName, projectionName).Set(float64(lag))
}

// RecordProjectionCheckpoint records the checkpoint position for a projection.
func (m *Metrics) RecordProjectionCheckpoint(projectionName string, position uint64) {
	m.projectionCheckpoint.WithLabelValues(m.serviceName, projectionName).Set(float64(position))
}

// RecordError records a custom error.
func (m *Metrics) RecordError(errorType string) {
	m.errorsTotal.WithLabelValues(m.serviceName, errorType).Inc()
}

// =============================================================================
// Getters for testing
// =============================================================================

// CommandsTotal returns the commands counter.
func (m *Metrics) CommandsTotal() *prometheus.CounterVec {
	return m.commandsTotal
}

// CommandDuration returns the command duration histogram.
func (m *Metrics) CommandDuration() *prometheus.HistogramVec {
	return m.commandDuration
}

// CommandsInFlight returns the in-flight commands gauge.
func (m *Metrics) CommandsInFlight() *prometheus.GaugeVec {
	return m.commandsInFlight
}

// EventStoreOperationsTotal returns the event store operations counter.
func (m *Metrics) EventStoreOperationsTotal() *prometheus.CounterVec {
	return m.eventStoreOperationsTotal
}

// EventStoreOperationDuration returns the event store duration histogram.
func (m *Metrics) EventStoreOperationDuration() *prometheus.HistogramVec {
	return m.eventStoreOperationDuration
}

// EventsAppendedTotal returns the events appended counter.
func (m *Metrics) EventsAppendedTotal() *prometheus.CounterVec {
	return m.eventsAppendedTotal
}

// EventsLoadedTotal returns the events loaded counter.
func (m *Metrics) EventsLoadedTotal() *prometheus.CounterVec {
	return m.eventsLoadedTotal
}

// ProjectionsProcessedTotal returns the projections processed counter.
func (m *Metrics) ProjectionsProcessedTotal() *prometheus.CounterVec {
	return m.projectionsProcessedTotal
}

// ProjectionDuration returns the projection duration histogram.
func (m *Metrics) ProjectionDuration() *prometheus.HistogramVec {
	return m.projectionDuration
}

// ProjectionLag returns the projection lag gauge.
func (m *Metrics) ProjectionLag() *prometheus.GaugeVec {
	return m.projectionLag
}

// ProjectionCheckpoint returns the projection checkpoint gauge.
func (m *Metrics) ProjectionCheckpoint() *prometheus.GaugeVec {
	return m.projectionCheckpoint
}

// ErrorsTotal returns the errors counter.
func (m *Metrics) ErrorsTotal() *prometheus.CounterVec {
	return m.errorsTotal
}

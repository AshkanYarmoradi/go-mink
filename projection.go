package mink

import (
	"context"
	"time"
)

// Projection is the base interface for all projection types.
// Projections transform events into optimized read models.
type Projection interface {
	// Name returns the unique identifier for this projection.
	// This name is used for checkpointing and management.
	Name() string

	// HandledEvents returns the list of event types this projection handles.
	// An empty list means the projection handles all event types.
	HandledEvents() []string
}

// InlineProjection processes events in the same transaction as the event store append.
// This provides strong consistency but may impact write performance.
type InlineProjection interface {
	Projection

	// Apply processes a single event within the event store transaction.
	// The projection should update its read model based on the event.
	Apply(ctx context.Context, event StoredEvent) error
}

// AsyncProjection processes events asynchronously in the background.
// This provides eventual consistency but better write performance and scalability.
type AsyncProjection interface {
	Projection

	// Apply processes a single event asynchronously.
	Apply(ctx context.Context, event StoredEvent) error

	// ApplyBatch processes multiple events in a single batch for efficiency.
	// If not supported, implementations should process events sequentially.
	ApplyBatch(ctx context.Context, events []StoredEvent) error
}

// LiveProjection receives events in real-time for dashboards and notifications.
// These projections are transient and don't persist state.
type LiveProjection interface {
	Projection

	// OnEvent is called for each event in real-time.
	// This method should not block for long periods.
	OnEvent(ctx context.Context, event StoredEvent)

	// IsTransient returns true if this projection doesn't persist state.
	// Transient projections are not checkpointed.
	IsTransient() bool
}

// ProjectionState represents the current state of a projection.
type ProjectionState string

const (
	// ProjectionStateStopped indicates the projection is not running.
	ProjectionStateStopped ProjectionState = "stopped"

	// ProjectionStateRunning indicates the projection is actively processing events.
	ProjectionStateRunning ProjectionState = "running"

	// ProjectionStatePaused indicates the projection is paused.
	ProjectionStatePaused ProjectionState = "paused"

	// ProjectionStateFaulted indicates the projection has encountered an error.
	ProjectionStateFaulted ProjectionState = "faulted"

	// ProjectionStateRebuilding indicates the projection is being rebuilt.
	ProjectionStateRebuilding ProjectionState = "rebuilding"

	// ProjectionStateCatchingUp indicates the projection is catching up to current events.
	ProjectionStateCatchingUp ProjectionState = "catching_up"
)

// ProjectionStatus provides detailed information about a projection's current state.
type ProjectionStatus struct {
	// Name is the projection name.
	Name string

	// State is the current state of the projection.
	State ProjectionState

	// LastPosition is the global position of the last processed event.
	LastPosition uint64

	// EventsProcessed is the total number of events processed.
	EventsProcessed uint64

	// LastProcessedAt is when the last event was processed.
	LastProcessedAt time.Time

	// Error contains the error message if the projection is faulted.
	Error string

	// Lag is the number of events behind the head of the event store.
	Lag uint64

	// AverageLatency is the average time to process an event.
	AverageLatency time.Duration
}

// CheckpointStore manages projection checkpoints.
// Checkpoints track the last processed position for each projection.
type CheckpointStore interface {
	// GetCheckpoint returns the last processed position for a projection.
	// Returns 0 if no checkpoint exists.
	GetCheckpoint(ctx context.Context, projectionName string) (uint64, error)

	// SetCheckpoint stores the last processed position for a projection.
	SetCheckpoint(ctx context.Context, projectionName string, position uint64) error

	// DeleteCheckpoint removes the checkpoint for a projection.
	DeleteCheckpoint(ctx context.Context, projectionName string) error

	// GetAllCheckpoints returns checkpoints for all projections.
	GetAllCheckpoints(ctx context.Context) (map[string]uint64, error)
}

// Checkpoint represents a stored checkpoint record.
type Checkpoint struct {
	// ProjectionName is the name of the projection.
	ProjectionName string

	// Position is the global position of the last processed event.
	Position uint64

	// UpdatedAt is when the checkpoint was last updated.
	UpdatedAt time.Time
}

// ProjectionMetrics collects metrics about projection processing.
type ProjectionMetrics interface {
	// RecordEventProcessed records that an event was processed.
	RecordEventProcessed(projectionName, eventType string, duration time.Duration, success bool)

	// RecordBatchProcessed records that a batch of events was processed.
	RecordBatchProcessed(projectionName string, count int, duration time.Duration, success bool)

	// RecordCheckpoint records a checkpoint update.
	RecordCheckpoint(projectionName string, position uint64)

	// RecordError records a projection error.
	RecordError(projectionName string, err error)
}

// noopProjectionMetrics is a no-op implementation of ProjectionMetrics.
type noopProjectionMetrics struct{}

func (m *noopProjectionMetrics) RecordEventProcessed(projectionName, eventType string, duration time.Duration, success bool) {
}

func (m *noopProjectionMetrics) RecordBatchProcessed(projectionName string, count int, duration time.Duration, success bool) {
}

func (m *noopProjectionMetrics) RecordCheckpoint(projectionName string, position uint64) {
}

func (m *noopProjectionMetrics) RecordError(projectionName string, err error) {
}

// ProjectionBase provides a default partial implementation of Projection.
// Embed this struct in your projection types to get common functionality.
type ProjectionBase struct {
	name          string
	handledEvents []string
}

// NewProjectionBase creates a new ProjectionBase.
func NewProjectionBase(name string, handledEvents ...string) ProjectionBase {
	return ProjectionBase{
		name:          name,
		handledEvents: handledEvents,
	}
}

// Name returns the projection name.
func (p *ProjectionBase) Name() string {
	return p.name
}

// HandledEvents returns the list of event types this projection handles.
func (p *ProjectionBase) HandledEvents() []string {
	return p.handledEvents
}

// HandlesEvent returns true if this projection handles the given event type.
func (p *ProjectionBase) HandlesEvent(eventType string) bool {
	if len(p.handledEvents) == 0 {
		return true // Empty list means handle all events
	}
	for _, et := range p.handledEvents {
		if et == eventType {
			return true
		}
	}
	return false
}

// AsyncProjectionBase provides a default implementation of AsyncProjection.
// Embed this struct and override Apply to create async projections.
type AsyncProjectionBase struct {
	ProjectionBase
}

// NewAsyncProjectionBase creates a new AsyncProjectionBase.
func NewAsyncProjectionBase(name string, handledEvents ...string) AsyncProjectionBase {
	return AsyncProjectionBase{
		ProjectionBase: NewProjectionBase(name, handledEvents...),
	}
}

// ApplyBatch provides a default implementation that processes events sequentially.
// Override this method for custom batch processing logic.
func (p *AsyncProjectionBase) ApplyBatch(ctx context.Context, events []StoredEvent) error {
	// Default implementation: process events sequentially
	// Subclasses can override for batch optimization
	return ErrNotImplemented
}

// LiveProjectionBase provides a default implementation of LiveProjection.
// Embed this struct and override OnEvent to create live projections.
type LiveProjectionBase struct {
	ProjectionBase
	transient bool
}

// NewLiveProjectionBase creates a new LiveProjectionBase.
func NewLiveProjectionBase(name string, transient bool, handledEvents ...string) LiveProjectionBase {
	return LiveProjectionBase{
		ProjectionBase: NewProjectionBase(name, handledEvents...),
		transient:      transient,
	}
}

// IsTransient returns whether this projection is transient.
func (p *LiveProjectionBase) IsTransient() bool {
	return p.transient
}

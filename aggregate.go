package mink

// Aggregate defines the interface for event-sourced aggregates.
// An aggregate is a domain object whose state is derived from a sequence of events.
type Aggregate interface {
	// AggregateID returns the unique identifier for this aggregate instance.
	AggregateID() string

	// AggregateType returns the type/category of this aggregate (e.g., "Order", "Customer").
	AggregateType() string

	// Version returns the current version of the aggregate.
	// This is the number of events that have been applied.
	Version() int64

	// ApplyEvent applies an event to update the aggregate's state.
	// This method should be idempotent and deterministic.
	ApplyEvent(event interface{}) error

	// UncommittedEvents returns events that have been applied but not yet persisted.
	UncommittedEvents() []interface{}

	// ClearUncommittedEvents removes all uncommitted events after successful persistence.
	ClearUncommittedEvents()
}

// AggregateBase provides a default partial implementation of the Aggregate interface.
// Embed this struct in your aggregate types to get default behavior.
type AggregateBase struct {
	id                string
	aggregateType     string
	version           int64
	uncommittedEvents []interface{}
}

// NewAggregateBase creates a new AggregateBase with the given ID and type.
func NewAggregateBase(id, aggregateType string) AggregateBase {
	return AggregateBase{
		id:            id,
		aggregateType: aggregateType,
	}
}

// AggregateID returns the aggregate's unique identifier.
func (a *AggregateBase) AggregateID() string {
	return a.id
}

// SetID sets the aggregate's ID.
func (a *AggregateBase) SetID(id string) {
	a.id = id
}

// AggregateType returns the aggregate type.
func (a *AggregateBase) AggregateType() string {
	return a.aggregateType
}

// SetType sets the aggregate type.
func (a *AggregateBase) SetType(t string) {
	a.aggregateType = t
}

// Version returns the current version of the aggregate.
func (a *AggregateBase) Version() int64 {
	return a.version
}

// SetVersion sets the aggregate version.
func (a *AggregateBase) SetVersion(v int64) {
	a.version = v
}

// IncrementVersion increments the aggregate version by 1.
func (a *AggregateBase) IncrementVersion() {
	a.version++
}

// UncommittedEvents returns events that haven't been persisted yet.
func (a *AggregateBase) UncommittedEvents() []interface{} {
	return a.uncommittedEvents
}

// ClearUncommittedEvents removes all uncommitted events.
func (a *AggregateBase) ClearUncommittedEvents() {
	a.uncommittedEvents = nil
}

// Apply records an event as uncommitted.
// This should be called by the aggregate after creating a new event.
// The aggregate should also update its internal state based on the event.
func (a *AggregateBase) Apply(event interface{}) {
	a.uncommittedEvents = append(a.uncommittedEvents, event)
}

// HasUncommittedEvents returns true if there are events waiting to be persisted.
func (a *AggregateBase) HasUncommittedEvents() bool {
	return len(a.uncommittedEvents) > 0
}

// StreamID returns the stream ID for this aggregate.
// The stream ID is composed of the aggregate type and ID.
func (a *AggregateBase) StreamID() StreamID {
	return NewStreamID(a.aggregateType, a.id)
}

// AggregateRoot is an extended interface that includes domain-driven design patterns.
type AggregateRoot interface {
	Aggregate

	// GetID is an alias for AggregateID for DDD conventions.
	GetID() string
}

// VersionedAggregate provides versioning information for optimistic concurrency.
type VersionedAggregate interface {
	Aggregate

	// OriginalVersion returns the version when the aggregate was loaded.
	OriginalVersion() int64
}

// EventApplier is a function type for applying events to aggregates.
type EventApplier func(aggregate interface{}, event interface{}) error

// AggregateFactory creates new aggregate instances.
type AggregateFactory func(id string) Aggregate

package mink

import (
	"fmt"
	"sync"
)

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
	originalVersion   int64
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

// GetID is an alias for AggregateID following DDD conventions.
// It lets aggregates embedding AggregateBase satisfy AggregateRoot.
func (a *AggregateBase) GetID() string {
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

// OriginalVersion returns the stream version the aggregate had when it was last
// loaded or saved (informational), and lets aggregates embedding AggregateBase
// satisfy VersionedAggregate. Note: SaveAggregate uses Version() — not this — for
// the optimistic-concurrency check; for stock AggregateBase the two are equal
// because creating uncommitted events does not change Version().
func (a *AggregateBase) OriginalVersion() int64 {
	return a.originalVersion
}

// setOriginalVersion records the persisted version. It is unexported so only the
// EventStore (same package) can set it via the originalVersionSetter interface
// during LoadAggregate/SaveAggregate.
func (a *AggregateBase) setOriginalVersion(v int64) {
	a.originalVersion = v
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

// VersionSetter is an optional interface that aggregates can implement to allow
// the EventStore to set their version during loading. This is used for optimistic
// concurrency control in SaveAggregate. AggregateBase implements this interface.
type VersionSetter interface {
	SetVersion(v int64)
}

// originalVersionSetter is implemented by AggregateBase so the EventStore can
// record the persisted version after load/save for optimistic concurrency.
type originalVersionSetter interface {
	setOriginalVersion(v int64)
}

// EventApplier is a function type for applying events to aggregates.
type EventApplier func(aggregate interface{}, event interface{}) error

// ReplayEvents applies a sequence of events to a target using the given
// EventApplier, stopping at the first error. It is a convenience for rebuilding
// state from events without an EventStore.
func ReplayEvents(applier EventApplier, target interface{}, events []interface{}) error {
	for _, e := range events {
		if err := applier(target, e); err != nil {
			return err
		}
	}
	return nil
}

// AggregateFactory creates new aggregate instances.
type AggregateFactory func(id string) Aggregate

// AggregateRegistry maps aggregate type names to factories, enabling generic
// construction of aggregates by type (e.g. for replay tooling or CLI commands).
// It is safe for concurrent use.
type AggregateRegistry struct {
	mu        sync.RWMutex
	factories map[string]AggregateFactory
}

// NewAggregateRegistry creates an empty AggregateRegistry.
func NewAggregateRegistry() *AggregateRegistry {
	return &AggregateRegistry{factories: make(map[string]AggregateFactory)}
}

// Register associates an aggregate type name with a factory.
func (r *AggregateRegistry) Register(aggregateType string, factory AggregateFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[aggregateType] = factory
}

// Create constructs a new aggregate of the given type with the given ID.
// Returns an error wrapping ErrAggregateTypeNotRegistered (with the type name)
// if no factory is registered for the type.
func (r *AggregateRegistry) Create(aggregateType, id string) (Aggregate, error) {
	r.mu.RLock()
	factory, ok := r.factories[aggregateType]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrAggregateTypeNotRegistered, aggregateType)
	}
	return factory(id), nil
}

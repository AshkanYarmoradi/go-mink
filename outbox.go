package mink

import (
	"context"
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// OutboxStatus represents the current status of an outbox message.
type OutboxStatus = adapters.OutboxStatus

// Outbox status constants.
const (
	OutboxPending    = adapters.OutboxPending
	OutboxProcessing = adapters.OutboxProcessing
	OutboxCompleted  = adapters.OutboxCompleted
	OutboxFailed     = adapters.OutboxFailed
	OutboxDeadLetter = adapters.OutboxDeadLetter
)

// OutboxMessage represents a message in the transactional outbox.
type OutboxMessage = adapters.OutboxMessage

// OutboxStore defines the interface for outbox message persistence.
type OutboxStore = adapters.OutboxStore

// Publisher publishes outbox messages to an external system.
type Publisher interface {
	// Publish sends one or more messages to the external system.
	Publish(ctx context.Context, messages []*OutboxMessage) error

	// Destination returns the destination prefix this publisher handles (e.g., "webhook", "kafka", "sns").
	Destination() string
}

// OutboxRoute defines routing rules for outbox messages.
type OutboxRoute struct {
	// EventTypes is the list of event types this route matches. Empty matches all.
	EventTypes []string

	// Destination is the target (e.g., "webhook:https://example.com/events", "kafka:orders").
	Destination string

	// Transform optionally transforms the event payload before outbox scheduling.
	Transform func(event interface{}, stored StoredEvent) ([]byte, error)

	// Filter optionally filters events. Return true to include the event.
	Filter func(event interface{}, stored StoredEvent) bool
}

// matchesEvent returns true if this route matches the given event type.
func (r *OutboxRoute) matchesEvent(eventType string) bool {
	if len(r.EventTypes) == 0 {
		return true
	}
	for _, et := range r.EventTypes {
		if et == eventType {
			return true
		}
	}
	return false
}

// OutboxMetrics collects metrics about outbox processing.
type OutboxMetrics interface {
	RecordMessageProcessed(destination string, success bool)
	RecordMessageFailed(destination string)
	RecordMessageDeadLettered()
	RecordBatchDuration(duration time.Duration)
	RecordPendingMessages(count int64)
}

// noopOutboxMetrics is a no-op implementation of OutboxMetrics.
type noopOutboxMetrics struct{}

func (m *noopOutboxMetrics) RecordMessageProcessed(destination string, success bool) {}
func (m *noopOutboxMetrics) RecordMessageFailed(destination string)                  {}
func (m *noopOutboxMetrics) RecordMessageDeadLettered()                              {}
func (m *noopOutboxMetrics) RecordBatchDuration(duration time.Duration)              {}
func (m *noopOutboxMetrics) RecordPendingMessages(count int64)                       {}

// EventStoreWithOutbox wraps an EventStore to automatically schedule outbox messages
// when events are appended. If the adapter implements OutboxAppender, events and outbox
// messages are written atomically in the same transaction.
type EventStoreWithOutbox struct {
	store       *EventStore
	outbox      OutboxStore
	routes      []OutboxRoute
	logger      Logger
	maxAttempts int
}

// OutboxOption configures an EventStoreWithOutbox.
type OutboxOption func(*EventStoreWithOutbox)

// WithOutboxLogger sets a logger for the outbox wrapper.
func WithOutboxLogger(l Logger) OutboxOption {
	return func(es *EventStoreWithOutbox) {
		es.logger = l
	}
}

// WithOutboxMaxAttempts sets the default max attempts for outbox messages.
func WithOutboxMaxAttempts(n int) OutboxOption {
	return func(es *EventStoreWithOutbox) {
		es.maxAttempts = n
	}
}

// NewEventStoreWithOutbox creates a new EventStoreWithOutbox wrapper.
func NewEventStoreWithOutbox(store *EventStore, outboxStore OutboxStore, routes []OutboxRoute, opts ...OutboxOption) *EventStoreWithOutbox {
	es := &EventStoreWithOutbox{
		store:       store,
		outbox:      outboxStore,
		routes:      routes,
		logger:      &noopLogger{},
		maxAttempts: 5,
	}
	for _, opt := range opts {
		opt(es)
	}
	return es
}

// Store returns the underlying EventStore.
func (es *EventStoreWithOutbox) Store() *EventStore {
	return es.store
}

// OutboxStore returns the underlying OutboxStore.
func (es *EventStoreWithOutbox) OutboxStore() OutboxStore {
	return es.outbox
}

// buildOutboxMessages creates outbox messages from stored events based on configured routes.
func (es *EventStoreWithOutbox) buildOutboxMessages(storedEvents []adapters.StoredEvent) []*OutboxMessage {
	var messages []*OutboxMessage
	now := time.Now()

	for _, se := range storedEvents {
		for _, route := range es.routes {
			if !route.matchesEvent(se.Type) {
				continue
			}

			payload := se.Data
			if route.Transform != nil {
				transformed, err := route.Transform(nil, convertStoredEventFromAdapter(se))
				if err != nil {
					es.logger.Error("Failed to transform outbox payload",
						"eventType", se.Type, "destination", route.Destination, "error", err)
					continue
				}
				payload = transformed
			}

			if route.Filter != nil && !route.Filter(nil, convertStoredEventFromAdapter(se)) {
				continue
			}

			messages = append(messages, &OutboxMessage{
				AggregateID: se.StreamID,
				EventType:   se.Type,
				Destination: route.Destination,
				Payload:     payload,
				Headers: map[string]string{
					"event-id":       se.ID,
					"stream-id":      se.StreamID,
					"event-type":     se.Type,
					"correlation-id": se.Metadata.CorrelationID,
					"causation-id":   se.Metadata.CausationID,
				},
				Status:      OutboxPending,
				MaxAttempts: es.maxAttempts,
				ScheduledAt: now,
				CreatedAt:   now,
			})
		}
	}

	return messages
}

// Append stores events and schedules outbox messages.
func (es *EventStoreWithOutbox) Append(ctx context.Context, streamID string, events []interface{}, opts ...AppendOption) error {
	if streamID == "" {
		return ErrEmptyStreamID
	}
	if len(events) == 0 {
		return ErrNoEvents
	}

	config := &appendConfig{
		expectedVersion: AnyVersion,
	}
	for _, opt := range opts {
		opt(config)
	}

	// Serialize events
	records := make([]adapters.EventRecord, len(events))
	for i, event := range events {
		eventData, err := SerializeEvent(es.store.serializer, event, config.metadata)
		if err != nil {
			return fmt.Errorf("mink: failed to serialize event %d: %w", i, err)
		}
		records[i] = adapters.EventRecord{
			Type:     eventData.Type,
			Data:     eventData.Data,
			Metadata: convertMetadataToAdapter(eventData.Metadata),
		}
	}

	// Build preliminary outbox messages (payload comes from serialized records)
	var prelimMessages []*OutboxMessage
	now := time.Now()
	for _, rec := range records {
		for _, route := range es.routes {
			if !route.matchesEvent(rec.Type) {
				continue
			}
			payload := rec.Data
			prelimMessages = append(prelimMessages, &OutboxMessage{
				AggregateID: streamID,
				EventType:   rec.Type,
				Destination: route.Destination,
				Payload:     payload,
				Headers: map[string]string{
					"stream-id":      streamID,
					"event-type":     rec.Type,
					"correlation-id": rec.Metadata.CorrelationID,
					"causation-id":   rec.Metadata.CausationID,
				},
				Status:      OutboxPending,
				MaxAttempts: es.maxAttempts,
				ScheduledAt: now,
				CreatedAt:   now,
			})
		}
	}

	// Try atomic append+outbox if adapter supports it
	if appender, ok := es.store.adapter.(adapters.OutboxAppender); ok && len(prelimMessages) > 0 {
		_, err := appender.AppendWithOutbox(ctx, streamID, records, config.expectedVersion, prelimMessages)
		return err
	}

	// Fallback: separate operations
	_, err := es.store.adapter.Append(ctx, streamID, records, config.expectedVersion)
	if err != nil {
		return err
	}

	if len(prelimMessages) > 0 {
		es.logger.Warn("Outbox messages scheduled non-atomically; adapter does not implement OutboxAppender")
		if err := es.outbox.Schedule(ctx, prelimMessages); err != nil {
			es.logger.Error("Failed to schedule outbox messages", "error", err)
			return fmt.Errorf("mink: events appended but outbox scheduling failed: %w", err)
		}
	}

	return nil
}

// SaveAggregate persists uncommitted events and schedules outbox messages.
func (es *EventStoreWithOutbox) SaveAggregate(ctx context.Context, agg Aggregate) error {
	if agg == nil {
		return ErrNilAggregate
	}

	events := agg.UncommittedEvents()
	if len(events) == 0 {
		return nil
	}

	streamID := fmt.Sprintf("%s-%s", agg.AggregateType(), agg.AggregateID())

	records := make([]adapters.EventRecord, len(events))
	for i, event := range events {
		eventData, err := SerializeEvent(es.store.serializer, event, Metadata{})
		if err != nil {
			return fmt.Errorf("mink: failed to serialize aggregate event %d: %w", i, err)
		}
		records[i] = adapters.EventRecord{
			Type:     eventData.Type,
			Data:     eventData.Data,
			Metadata: convertMetadataToAdapter(eventData.Metadata),
		}
	}

	expectedVersion := agg.Version()

	// Build outbox messages
	var outboxMessages []*OutboxMessage
	now := time.Now()
	for _, rec := range records {
		for _, route := range es.routes {
			if !route.matchesEvent(rec.Type) {
				continue
			}
			payload := rec.Data
			outboxMessages = append(outboxMessages, &OutboxMessage{
				AggregateID: streamID,
				EventType:   rec.Type,
				Destination: route.Destination,
				Payload:     payload,
				Headers: map[string]string{
					"stream-id":      streamID,
					"event-type":     rec.Type,
					"correlation-id": rec.Metadata.CorrelationID,
					"causation-id":   rec.Metadata.CausationID,
				},
				Status:      OutboxPending,
				MaxAttempts: es.maxAttempts,
				ScheduledAt: now,
				CreatedAt:   now,
			})
		}
	}

	// Try atomic append+outbox
	if appender, ok := es.store.adapter.(adapters.OutboxAppender); ok && len(outboxMessages) > 0 {
		_, err := appender.AppendWithOutbox(ctx, streamID, records, expectedVersion, outboxMessages)
		if err != nil {
			return err
		}
	} else {
		_, err := es.store.adapter.Append(ctx, streamID, records, expectedVersion)
		if err != nil {
			return err
		}

		if len(outboxMessages) > 0 {
			es.logger.Warn("Outbox messages scheduled non-atomically; adapter does not implement OutboxAppender")
			if err := es.outbox.Schedule(ctx, outboxMessages); err != nil {
				es.logger.Error("Failed to schedule outbox messages", "error", err)
			}
		}
	}

	// Update aggregate version
	if setter, ok := agg.(VersionSetter); ok {
		setter.SetVersion(expectedVersion + int64(len(events)))
	}

	agg.ClearUncommittedEvents()
	return nil
}

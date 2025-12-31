package mink

import (
	"context"
	"sync"
	"time"
)

// Subscription represents an active event subscription.
type Subscription interface {
	// Events returns the channel for receiving events.
	Events() <-chan StoredEvent

	// Close stops the subscription.
	Close() error

	// Err returns any error that caused the subscription to close.
	Err() error
}

// SubscriptionOptions configures a subscription.
type SubscriptionOptions struct {
	// BufferSize is the size of the event channel buffer.
	// Default: 256
	BufferSize int

	// Filter optionally filters which events are delivered.
	Filter EventFilter

	// RetryOnError determines whether to retry on transient errors.
	// Default: true
	RetryOnError bool

	// RetryInterval is the time to wait between retries.
	// Default: 1 second
	RetryInterval time.Duration

	// MaxRetries is the maximum number of retry attempts.
	// Default: 5
	MaxRetries int
}

// DefaultSubscriptionOptions returns the default subscription options.
func DefaultSubscriptionOptions() SubscriptionOptions {
	return SubscriptionOptions{
		BufferSize:    256,
		RetryOnError:  true,
		RetryInterval: time.Second,
		MaxRetries:    5,
	}
}

// EventFilter determines which events should be delivered.
type EventFilter interface {
	// Matches returns true if the event should be delivered.
	Matches(event StoredEvent) bool
}

// EventTypeFilter filters events by type.
type EventTypeFilter struct {
	eventTypes map[string]struct{}
}

// NewEventTypeFilter creates a filter that only matches the specified event types.
func NewEventTypeFilter(eventTypes ...string) *EventTypeFilter {
	f := &EventTypeFilter{
		eventTypes: make(map[string]struct{}, len(eventTypes)),
	}
	for _, t := range eventTypes {
		f.eventTypes[t] = struct{}{}
	}
	return f
}

// Matches returns true if the event type is in the filter.
func (f *EventTypeFilter) Matches(event StoredEvent) bool {
	_, ok := f.eventTypes[event.Type]
	return ok
}

// CategoryFilter filters events by stream category.
type CategoryFilter struct {
	category string
}

// NewCategoryFilter creates a filter that only matches events from streams in the category.
func NewCategoryFilter(category string) *CategoryFilter {
	return &CategoryFilter{category: category}
}

// Matches returns true if the event's stream is in the category.
func (f *CategoryFilter) Matches(event StoredEvent) bool {
	streamID, err := ParseStreamID(event.StreamID)
	if err != nil {
		return false
	}
	return streamID.Category == f.category
}

// CompositeFilter combines multiple filters with AND logic.
type CompositeFilter struct {
	filters []EventFilter
}

// NewCompositeFilter creates a filter that matches only if all filters match.
func NewCompositeFilter(filters ...EventFilter) *CompositeFilter {
	return &CompositeFilter{filters: filters}
}

// Matches returns true if all filters match.
func (f *CompositeFilter) Matches(event StoredEvent) bool {
	for _, filter := range f.filters {
		if !filter.Matches(event) {
			return false
		}
	}
	return true
}

// EventSubscriber provides event subscription capabilities.
type EventSubscriber interface {
	// SubscribeAll subscribes to all events starting from the given position.
	SubscribeAll(ctx context.Context, fromPosition uint64, opts ...SubscriptionOptions) (Subscription, error)

	// SubscribeStream subscribes to events from a specific stream.
	SubscribeStream(ctx context.Context, streamID string, fromVersion int64, opts ...SubscriptionOptions) (Subscription, error)

	// SubscribeCategory subscribes to events from all streams in a category.
	SubscribeCategory(ctx context.Context, category string, fromPosition uint64, opts ...SubscriptionOptions) (Subscription, error)
}

// CatchupSubscription provides catch-up subscription functionality.
// It first reads historical events from the event store, then switches to
// polling for new events. This ensures no events are missed during the transition.
type CatchupSubscription struct {
	store    *EventStore
	opts     SubscriptionOptions
	position uint64

	eventCh chan StoredEvent
	stopCh  chan struct{}
	errMu   sync.RWMutex
	err     error
	closed  bool
	started bool
}

// NewCatchupSubscription creates a new catch-up subscription.
// Call Start() to begin receiving events from the specified position.
func NewCatchupSubscription(
	store *EventStore,
	fromPosition uint64,
	opts ...SubscriptionOptions,
) (*CatchupSubscription, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	options := DefaultSubscriptionOptions()
	if len(opts) > 0 {
		options = opts[0]
	}

	s := &CatchupSubscription{
		store:    store,
		opts:     options,
		position: fromPosition,
		eventCh:  make(chan StoredEvent, options.BufferSize),
		stopCh:   make(chan struct{}),
	}

	return s, nil
}

// Start begins the catch-up subscription with the specified poll interval.
// It first catches up on historical events, then polls for new events.
func (s *CatchupSubscription) Start(ctx context.Context, pollInterval time.Duration) error {
	s.errMu.Lock()
	if s.started {
		s.errMu.Unlock()
		return nil
	}
	if s.closed {
		s.errMu.Unlock()
		return ErrAdapterClosed
	}
	s.started = true
	s.errMu.Unlock()

	go s.run(ctx, pollInterval)
	return nil
}

func (s *CatchupSubscription) run(ctx context.Context, pollInterval time.Duration) {
	defer close(s.eventCh)

	// Get subscription adapter
	adapter := s.store.Adapter()
	subAdapter, ok := adapter.(SubscriptionAdapter)
	if !ok {
		s.setErr(ErrSubscriptionNotSupported)
		return
	}

	// Phase 1: Catch up on historical events
	for {
		select {
		case <-ctx.Done():
			s.setErr(ctx.Err())
			return
		case <-s.stopCh:
			return
		default:
		}

		events, err := subAdapter.LoadFromPosition(ctx, s.position, 100)
		if err != nil {
			s.setErr(err)
			return
		}

		if len(events) == 0 {
			break // Caught up, switch to polling
		}

		for _, event := range events {
			if s.opts.Filter != nil && !s.opts.Filter.Matches(event) {
				s.position = event.GlobalPosition
				continue
			}

			select {
			case s.eventCh <- event:
				s.position = event.GlobalPosition
			case <-ctx.Done():
				s.setErr(ctx.Err())
				return
			case <-s.stopCh:
				return
			}
		}
	}

	// Phase 2: Poll for new events
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.setErr(ctx.Err())
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			events, err := subAdapter.LoadFromPosition(ctx, s.position, 100)
			if err != nil {
				// Retry on error unless configured not to
				if !s.opts.RetryOnError {
					s.setErr(err)
					return
				}
				continue
			}

			for _, event := range events {
				if s.opts.Filter != nil && !s.opts.Filter.Matches(event) {
					s.position = event.GlobalPosition
					continue
				}

				select {
				case s.eventCh <- event:
					s.position = event.GlobalPosition
				case <-ctx.Done():
					s.setErr(ctx.Err())
					return
				case <-s.stopCh:
					return
				}
			}
		}
	}
}

// Events returns the channel for receiving events.
func (s *CatchupSubscription) Events() <-chan StoredEvent {
	return s.eventCh
}

// Close stops the subscription.
func (s *CatchupSubscription) Close() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	close(s.stopCh)
	return nil
}

// Err returns any error that caused the subscription to close.
func (s *CatchupSubscription) Err() error {
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	return s.err
}

// Position returns the current position of the subscription.
func (s *CatchupSubscription) Position() uint64 {
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	return s.position
}

func (s *CatchupSubscription) setErr(err error) {
	s.errMu.Lock()
	s.err = err
	s.errMu.Unlock()
}

// PollingSubscription polls the event store for new events.
// This is a fallback when push-based subscriptions aren't available.
type PollingSubscription struct {
	store *EventStore
	opts  SubscriptionOptions

	eventCh  chan StoredEvent
	stopCh   chan struct{}
	position uint64
	errMu    sync.RWMutex
	err      error
	closed   bool
}

// NewPollingSubscription creates a new polling subscription.
func NewPollingSubscription(
	store *EventStore,
	fromPosition uint64,
	opts ...SubscriptionOptions,
) *PollingSubscription {
	options := DefaultSubscriptionOptions()
	if len(opts) > 0 {
		options = opts[0]
	}

	return &PollingSubscription{
		store:    store,
		opts:     options,
		eventCh:  make(chan StoredEvent, options.BufferSize),
		stopCh:   make(chan struct{}),
		position: fromPosition,
	}
}

// Start begins polling for events.
func (s *PollingSubscription) Start(ctx context.Context, pollInterval time.Duration) {
	go s.poll(ctx, pollInterval)
}

func (s *PollingSubscription) poll(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(s.eventCh)

	for {
		select {
		case <-ctx.Done():
			s.setErr(ctx.Err())
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			// Load events from current position using SubscriptionAdapter if available
			adapter := s.store.Adapter()
			subAdapter, ok := adapter.(SubscriptionAdapter)
			if !ok {
				// Adapter doesn't support subscription, skip this poll cycle
				continue
			}

			events, err := subAdapter.LoadFromPosition(ctx, s.position, 100)
			if err != nil {
				// On error, continue polling (could add retry logic here)
				continue
			}

			for _, event := range events {
				// Apply filter if configured
				if s.opts.Filter != nil && !s.opts.Filter.Matches(event) {
					s.position = event.GlobalPosition
					continue
				}

				select {
				case s.eventCh <- event:
					s.position = event.GlobalPosition
				case <-ctx.Done():
					s.setErr(ctx.Err())
					return
				case <-s.stopCh:
					return
				}
			}
		}
	}
}

// Events returns the channel for receiving events.
func (s *PollingSubscription) Events() <-chan StoredEvent {
	return s.eventCh
}

// Close stops the subscription.
func (s *PollingSubscription) Close() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	close(s.stopCh)
	return nil
}

// Err returns any error that caused the subscription to close.
func (s *PollingSubscription) Err() error {
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	return s.err
}

func (s *PollingSubscription) setErr(err error) {
	s.errMu.Lock()
	s.err = err
	s.errMu.Unlock()
}

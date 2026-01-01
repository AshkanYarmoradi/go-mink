// Package memory provides an in-memory implementation of the event store adapter.
// This adapter is primarily intended for testing and development purposes.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/google/uuid"
)

// Version constants for optimistic concurrency control.
// These are re-exported from the adapters package for convenience.
const (
	AnyVersion   = adapters.AnyVersion
	NoStream     = adapters.NoStream
	StreamExists = adapters.StreamExists
)

// Ensure MemoryAdapter implements all required interfaces.
var (
	_ adapters.EventStoreAdapter   = (*MemoryAdapter)(nil)
	_ adapters.SubscriptionAdapter = (*MemoryAdapter)(nil)
	_ adapters.SnapshotAdapter     = (*MemoryAdapter)(nil)
	_ adapters.CheckpointAdapter   = (*MemoryAdapter)(nil)
	_ adapters.HealthChecker       = (*MemoryAdapter)(nil)
)

// Default values for subscriptions.
const defaultSubscriptionBuffer = 100

// getBufferSize extracts buffer size from options or returns the default.
func getBufferSize(opts []adapters.SubscriptionOptions) int {
	if len(opts) > 0 && opts[0].BufferSize > 0 {
		return opts[0].BufferSize
	}
	return defaultSubscriptionBuffer
}

// filterEvents creates a filtered event channel from a source channel.
// This is a shared helper used by SubscribeStream and SubscribeCategory.
func filterEvents(ctx context.Context, source <-chan adapters.StoredEvent, bufferSize int, filter func(adapters.StoredEvent) bool) <-chan adapters.StoredEvent {
	ch := make(chan adapters.StoredEvent, bufferSize)
	go func() {
		defer close(ch)
		for event := range source {
			if filter(event) {
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}

// MemoryAdapter is an in-memory implementation of EventStoreAdapter.
// It is thread-safe and suitable for unit testing.
type MemoryAdapter struct {
	mu             sync.RWMutex
	streams        map[string]*streamData
	globalEvents   []adapters.StoredEvent
	globalPosition uint64
	snapshots      map[string]*adapters.SnapshotRecord
	checkpoints    map[string]uint64
	closed         bool

	// Subscribers for real-time notifications
	subscribers   []chan adapters.StoredEvent
	subscribersMu sync.RWMutex
}

type streamData struct {
	info   adapters.StreamInfo
	events []adapters.StoredEvent
}

// Option configures a MemoryAdapter.
type Option func(*MemoryAdapter)

// NewAdapter creates a new in-memory event store adapter.
func NewAdapter(opts ...Option) *MemoryAdapter {
	adapter := &MemoryAdapter{
		streams:      make(map[string]*streamData),
		globalEvents: make([]adapters.StoredEvent, 0),
		snapshots:    make(map[string]*adapters.SnapshotRecord),
		checkpoints:  make(map[string]uint64),
		subscribers:  make([]chan adapters.StoredEvent, 0),
	}

	for _, opt := range opts {
		opt(adapter)
	}

	return adapter
}

// Initialize is a no-op for the memory adapter.
func (a *MemoryAdapter) Initialize(ctx context.Context) error {
	return nil
}

// Append stores events to the specified stream with optimistic concurrency control.
func (a *MemoryAdapter) Append(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	if streamID == "" {
		return nil, adapters.ErrEmptyStreamID
	}

	if len(events) == 0 {
		return nil, adapters.ErrNoEvents
	}

	// Get or create stream
	stream, exists := a.streams[streamID]
	currentVersion := int64(0)
	if exists {
		currentVersion = stream.info.Version
	}

	// Check expected version
	if err := adapters.CheckVersion(streamID, expectedVersion, currentVersion, exists); err != nil {
		return nil, err
	}

	// Create stream if it doesn't exist
	if !exists {
		category := adapters.ExtractCategory(streamID)
		stream = &streamData{
			info: adapters.StreamInfo{
				StreamID:  streamID,
				Category:  category,
				Version:   0,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
			events: make([]adapters.StoredEvent, 0),
		}
		a.streams[streamID] = stream
	}

	// Append events
	now := time.Now()
	storedEvents := make([]adapters.StoredEvent, len(events))

	for i, event := range events {
		a.globalPosition++
		currentVersion++

		stored := adapters.StoredEvent{
			ID:             uuid.New().String(),
			StreamID:       streamID,
			Type:           event.Type,
			Data:           event.Data,
			Metadata:       event.Metadata,
			Version:        currentVersion,
			GlobalPosition: a.globalPosition,
			Timestamp:      now,
		}

		stream.events = append(stream.events, stored)
		a.globalEvents = append(a.globalEvents, stored)
		storedEvents[i] = stored
	}

	// Update stream info
	stream.info.Version = currentVersion
	stream.info.EventCount = int64(len(stream.events))
	stream.info.UpdatedAt = now

	// Notify subscribers (non-blocking)
	a.notifySubscribers(storedEvents)

	return storedEvents, nil
}

// Load retrieves all events from a stream starting from the specified version.
func (a *MemoryAdapter) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	if streamID == "" {
		return nil, adapters.ErrEmptyStreamID
	}

	stream, exists := a.streams[streamID]
	if !exists {
		return []adapters.StoredEvent{}, nil
	}

	// Filter events by version
	events := make([]adapters.StoredEvent, 0)
	for _, event := range stream.events {
		if event.Version > fromVersion {
			events = append(events, event)
		}
	}

	return events, nil
}

// GetStreamInfo returns metadata about a stream.
func (a *MemoryAdapter) GetStreamInfo(ctx context.Context, streamID string) (*adapters.StreamInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	stream, exists := a.streams[streamID]
	if !exists {
		return nil, NewStreamNotFoundError(streamID)
	}

	// Return a copy to prevent mutation
	info := stream.info
	return &info, nil
}

// GetLastPosition returns the global position of the last stored event.
func (a *MemoryAdapter) GetLastPosition(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return 0, ErrAdapterClosed
	}

	return a.globalPosition, nil
}

// Close releases any resources held by the adapter.
func (a *MemoryAdapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.closed = true

	// Close all subscriber channels
	a.subscribersMu.Lock()
	for _, ch := range a.subscribers {
		close(ch)
	}
	a.subscribers = nil
	a.subscribersMu.Unlock()

	return nil
}

// LoadFromPosition loads events starting from a global position.
// This is used by projection engines to catch up on historical events.
func (a *MemoryAdapter) LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]adapters.StoredEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	limit = adapters.DefaultLimit(limit, 1000)

	var events []adapters.StoredEvent
	for _, event := range a.globalEvents {
		if event.GlobalPosition > fromPosition {
			events = append(events, event)
			if len(events) >= limit {
				break
			}
		}
	}

	return events, nil
}

// SubscribeAll subscribes to all events across all streams.
func (a *MemoryAdapter) SubscribeAll(ctx context.Context, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	if a.closed {
		a.mu.RUnlock()
		return nil, adapters.ErrAdapterClosed
	}

	// Apply options
	bufferSize := getBufferSize(opts)

	// Create buffered channel for subscriber
	ch := make(chan adapters.StoredEvent, bufferSize)

	// Send historical events
	for _, event := range a.globalEvents {
		if event.GlobalPosition > fromPosition {
			select {
			case ch <- event:
			case <-ctx.Done():
				close(ch)
				a.mu.RUnlock()
				return nil, ctx.Err()
			}
		}
	}
	a.mu.RUnlock()

	// Register for future events
	a.subscribersMu.Lock()
	a.subscribers = append(a.subscribers, ch)
	a.subscribersMu.Unlock()

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		a.removeSubscriber(ch)
	}()

	return ch, nil
}

// SubscribeStream subscribes to events from a specific stream.
func (a *MemoryAdapter) SubscribeStream(ctx context.Context, streamID string, fromVersion int64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	allEvents, err := a.SubscribeAll(ctx, 0, opts...)
	if err != nil {
		return nil, err
	}

	bufferSize := getBufferSize(opts)
	return filterEvents(ctx, allEvents, bufferSize, func(e adapters.StoredEvent) bool {
		return e.StreamID == streamID && e.Version > fromVersion
	}), nil
}

// SubscribeCategory subscribes to all events from streams in a category.
func (a *MemoryAdapter) SubscribeCategory(ctx context.Context, category string, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	allEvents, err := a.SubscribeAll(ctx, fromPosition, opts...)
	if err != nil {
		return nil, err
	}

	bufferSize := getBufferSize(opts)
	return filterEvents(ctx, allEvents, bufferSize, func(e adapters.StoredEvent) bool {
		return adapters.ExtractCategory(e.StreamID) == category
	}), nil
}

// SaveSnapshot stores a snapshot for the given stream.
func (a *MemoryAdapter) SaveSnapshot(ctx context.Context, streamID string, version int64, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return ErrAdapterClosed
	}

	a.snapshots[streamID] = &adapters.SnapshotRecord{
		StreamID: streamID,
		Version:  version,
		Data:     data,
	}

	return nil
}

// LoadSnapshot retrieves the latest snapshot for the given stream.
func (a *MemoryAdapter) LoadSnapshot(ctx context.Context, streamID string) (*adapters.SnapshotRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	snapshot, exists := a.snapshots[streamID]
	if !exists {
		return nil, nil
	}

	// Return a copy
	return &adapters.SnapshotRecord{
		StreamID: snapshot.StreamID,
		Version:  snapshot.Version,
		Data:     snapshot.Data,
	}, nil
}

// DeleteSnapshot removes the snapshot for the given stream.
func (a *MemoryAdapter) DeleteSnapshot(ctx context.Context, streamID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return ErrAdapterClosed
	}

	delete(a.snapshots, streamID)
	return nil
}

// GetCheckpoint returns the last processed position for a projection.
func (a *MemoryAdapter) GetCheckpoint(ctx context.Context, projectionName string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return 0, ErrAdapterClosed
	}

	return a.checkpoints[projectionName], nil
}

// SetCheckpoint stores the last processed position for a projection.
func (a *MemoryAdapter) SetCheckpoint(ctx context.Context, projectionName string, position uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return ErrAdapterClosed
	}

	a.checkpoints[projectionName] = position
	return nil
}

// Ping checks if the adapter is healthy.
func (a *MemoryAdapter) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return adapters.ErrAdapterClosed
	}

	return nil
}

// Reset clears all data. Useful for testing.
func (a *MemoryAdapter) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.streams = make(map[string]*streamData)
	a.globalEvents = make([]adapters.StoredEvent, 0)
	a.globalPosition = 0
	a.snapshots = make(map[string]*adapters.SnapshotRecord)
	a.checkpoints = make(map[string]uint64)
}

// EventCount returns the total number of events stored.
func (a *MemoryAdapter) EventCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.globalEvents)
}

// StreamCount returns the number of streams.
func (a *MemoryAdapter) StreamCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.streams)
}

// notifySubscribers sends events to all subscribers.
func (a *MemoryAdapter) notifySubscribers(events []adapters.StoredEvent) {
	a.subscribersMu.RLock()
	defer a.subscribersMu.RUnlock()

	for _, ch := range a.subscribers {
		for _, event := range events {
			select {
			case ch <- event:
			default:
				// Channel full, skip (non-blocking)
			}
		}
	}
}

// removeSubscriber removes a subscriber channel.
func (a *MemoryAdapter) removeSubscriber(ch chan adapters.StoredEvent) {
	a.subscribersMu.Lock()
	defer a.subscribersMu.Unlock()

	for i, subscriber := range a.subscribers {
		if subscriber == ch {
			a.subscribers = append(a.subscribers[:i], a.subscribers[i+1:]...)
			close(ch)
			break
		}
	}
}

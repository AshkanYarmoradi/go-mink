// Package memory provides an in-memory implementation of the event store adapter.
// This adapter is primarily intended for testing and development purposes.
package memory

import (
	"context"
	"strconv"
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

// Ensure MemoryAdapter implements CLI-related interfaces.
var (
	_ adapters.StreamQueryAdapter     = (*MemoryAdapter)(nil)
	_ adapters.ProjectionQueryAdapter = (*MemoryAdapter)(nil)
	_ adapters.MigrationAdapter       = (*MemoryAdapter)(nil)
	_ adapters.SchemaProvider         = (*MemoryAdapter)(nil)
)

// projectionInfo holds internal projection state for the memory adapter.
type projectionInfo struct {
	name      string
	position  int64
	status    string
	updatedAt time.Time
}

// migrationRecord holds internal migration state for the memory adapter.
type migrationRecord struct {
	name      string
	appliedAt time.Time
}

// ListStreams returns a list of stream summaries for CLI display.
func (a *MemoryAdapter) ListStreams(ctx context.Context, prefix string, limit int) ([]adapters.StreamSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	var summaries []adapters.StreamSummary
	for _, stream := range a.streams {
		if prefix != "" && !hasPrefix(stream.info.StreamID, prefix) {
			continue
		}

		lastEventType := ""
		if len(stream.events) > 0 {
			lastEventType = stream.events[len(stream.events)-1].Type
		}

		summaries = append(summaries, adapters.StreamSummary{
			StreamID:      stream.info.StreamID,
			EventCount:    stream.info.EventCount,
			LastEventType: lastEventType,
			LastUpdated:   stream.info.UpdatedAt,
		})
	}

	// Sort by last updated descending
	sortStreamSummaries(summaries)

	if limit > 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}

	return summaries, nil
}

// hasPrefix checks if a string has the given prefix.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// sortStreamSummaries sorts summaries by LastUpdated descending.
func sortStreamSummaries(summaries []adapters.StreamSummary) {
	// Simple bubble sort for small data sets (typical in memory adapter)
	for i := 0; i < len(summaries); i++ {
		for j := i + 1; j < len(summaries); j++ {
			if summaries[i].LastUpdated.Before(summaries[j].LastUpdated) {
				summaries[i], summaries[j] = summaries[j], summaries[i]
			}
		}
	}
}

// GetStreamEvents returns events from a stream with pagination for CLI display.
func (a *MemoryAdapter) GetStreamEvents(ctx context.Context, streamID string, fromVersion int64, limit int) ([]adapters.StoredEvent, error) {
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
		return nil, nil
	}

	var events []adapters.StoredEvent
	for _, event := range stream.events {
		if event.Version > fromVersion {
			events = append(events, event)
			if limit > 0 && len(events) >= limit {
				break
			}
		}
	}

	return events, nil
}

// GetEventStoreStats returns aggregate statistics about the event store.
func (a *MemoryAdapter) GetEventStoreStats(ctx context.Context) (*adapters.EventStoreStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	stats := &adapters.EventStoreStats{
		TotalEvents:  int64(len(a.globalEvents)),
		TotalStreams: int64(len(a.streams)),
	}

	// Count event types
	typeCount := make(map[string]int64)
	for _, event := range a.globalEvents {
		typeCount[event.Type]++
	}
	stats.EventTypes = int64(len(typeCount))

	if stats.TotalStreams > 0 {
		stats.AvgEventsPerStream = float64(stats.TotalEvents) / float64(stats.TotalStreams)
	}

	// Top event types (sorted by count)
	for eventType, count := range typeCount {
		stats.TopEventTypes = append(stats.TopEventTypes, adapters.EventTypeCount{
			Type:  eventType,
			Count: count,
		})
	}
	sortEventTypeCounts(stats.TopEventTypes)
	if len(stats.TopEventTypes) > 5 {
		stats.TopEventTypes = stats.TopEventTypes[:5]
	}

	return stats, nil
}

// sortEventTypeCounts sorts by count descending.
func sortEventTypeCounts(counts []adapters.EventTypeCount) {
	for i := 0; i < len(counts); i++ {
		for j := i + 1; j < len(counts); j++ {
			if counts[i].Count < counts[j].Count {
				counts[i], counts[j] = counts[j], counts[i]
			}
		}
	}
}

// projections holds projection info for the memory adapter.
var memoryProjections = make(map[string]*projectionInfo)
var memoryProjectionsMu sync.RWMutex

// ListProjections returns all registered projections.
func (a *MemoryAdapter) ListProjections(ctx context.Context) ([]adapters.ProjectionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	memoryProjectionsMu.RLock()
	defer memoryProjectionsMu.RUnlock()

	var projections []adapters.ProjectionInfo
	for _, p := range memoryProjections {
		projections = append(projections, adapters.ProjectionInfo{
			Name:      p.name,
			Position:  p.position,
			Status:    p.status,
			UpdatedAt: p.updatedAt,
		})
	}

	return projections, nil
}

// GetProjection returns information about a specific projection.
func (a *MemoryAdapter) GetProjection(ctx context.Context, name string) (*adapters.ProjectionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	memoryProjectionsMu.RLock()
	defer memoryProjectionsMu.RUnlock()

	p, exists := memoryProjections[name]
	if !exists {
		return nil, nil
	}

	return &adapters.ProjectionInfo{
		Name:      p.name,
		Position:  p.position,
		Status:    p.status,
		UpdatedAt: p.updatedAt,
	}, nil
}

// SetProjectionStatus updates a projection's status.
func (a *MemoryAdapter) SetProjectionStatus(ctx context.Context, name string, status string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.RLock()
	if a.closed {
		a.mu.RUnlock()
		return adapters.ErrAdapterClosed
	}
	a.mu.RUnlock()

	memoryProjectionsMu.Lock()
	defer memoryProjectionsMu.Unlock()

	p, exists := memoryProjections[name]
	if !exists {
		return adapters.ErrStreamNotFound
	}

	p.status = status
	p.updatedAt = time.Now()

	return nil
}

// ResetProjectionCheckpoint resets a projection's position to 0 for rebuild.
func (a *MemoryAdapter) ResetProjectionCheckpoint(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return adapters.ErrAdapterClosed
	}

	a.checkpoints[name] = 0

	memoryProjectionsMu.Lock()
	defer memoryProjectionsMu.Unlock()

	if p, exists := memoryProjections[name]; exists {
		p.position = 0
		p.updatedAt = time.Now()
	}

	return nil
}

// GetTotalEventCount returns the highest global position.
func (a *MemoryAdapter) GetTotalEventCount(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return 0, adapters.ErrAdapterClosed
	}

	return int64(a.globalPosition), nil
}

// migrations holds migration records for the memory adapter.
var memoryMigrations = make(map[string]*migrationRecord)
var memoryMigrationsMu sync.RWMutex

// GetAppliedMigrations returns the list of applied migration names.
func (a *MemoryAdapter) GetAppliedMigrations(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	memoryMigrationsMu.RLock()
	defer memoryMigrationsMu.RUnlock()

	var names []string
	for name := range memoryMigrations {
		names = append(names, name)
	}

	// Sort migration names
	sortStrings(names)

	return names, nil
}

// sortStrings sorts strings in ascending order.
func sortStrings(s []string) {
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[i] > s[j] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// RecordMigration marks a migration as applied.
func (a *MemoryAdapter) RecordMigration(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.RLock()
	if a.closed {
		a.mu.RUnlock()
		return adapters.ErrAdapterClosed
	}
	a.mu.RUnlock()

	memoryMigrationsMu.Lock()
	defer memoryMigrationsMu.Unlock()

	memoryMigrations[name] = &migrationRecord{
		name:      name,
		appliedAt: time.Now(),
	}

	return nil
}

// RemoveMigrationRecord removes a migration record (for rollback).
func (a *MemoryAdapter) RemoveMigrationRecord(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.RLock()
	if a.closed {
		a.mu.RUnlock()
		return adapters.ErrAdapterClosed
	}
	a.mu.RUnlock()

	memoryMigrationsMu.Lock()
	defer memoryMigrationsMu.Unlock()

	delete(memoryMigrations, name)

	return nil
}

// ExecuteSQL is a no-op for the memory adapter (no SQL to execute).
func (a *MemoryAdapter) ExecuteSQL(ctx context.Context, sql string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return adapters.ErrAdapterClosed
	}

	// Memory adapter doesn't execute SQL - this is a no-op
	return nil
}

// GenerateSchema returns an informational message for the memory adapter.
func (a *MemoryAdapter) GenerateSchema(projectName, tableName, snapshotTableName, outboxTableName string) string {
	return `-- Mink Event Store (In-Memory)
-- Generated for: ` + projectName + `

-- The memory adapter does not require schema creation.
-- All data is stored in-memory and will be lost when the application stops.
-- This adapter is intended for testing and development only.
--
-- For production use, please use the PostgreSQL adapter:
--   mink init --driver=postgres
`
}

// ============================================================================
// DiagnosticAdapter Implementation
// ============================================================================

// GetDiagnosticInfo returns diagnostic information for the memory adapter.
func (a *MemoryAdapter) GetDiagnosticInfo(ctx context.Context) (*adapters.DiagnosticInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	return &adapters.DiagnosticInfo{
		Connected: true,
		Version:   "In-Memory Adapter v1.0",
		Message:   "Using in-memory storage (no database connection needed)",
	}, nil
}

// CheckSchema verifies the event store "schema" (always exists for memory).
func (a *MemoryAdapter) CheckSchema(ctx context.Context, tableName string) (*adapters.SchemaCheckResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	// Count all events
	var totalEvents int64
	for _, stream := range a.streams {
		totalEvents += int64(len(stream.events))
	}

	message := "In-memory storage active"
	if totalEvents > 0 {
		message = "In-memory storage active (" + strconv.FormatInt(totalEvents, 10) + " events)"
	}

	return &adapters.SchemaCheckResult{
		TableExists: true,
		EventCount:  totalEvents,
		Message:     message,
	}, nil
}

// GetProjectionHealth returns projection health status for memory adapter.
func (a *MemoryAdapter) GetProjectionHealth(ctx context.Context) (*adapters.ProjectionHealthResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.closed {
		return nil, adapters.ErrAdapterClosed
	}

	memoryProjectionsMu.RLock()
	defer memoryProjectionsMu.RUnlock()

	result := &adapters.ProjectionHealthResult{
		TotalProjections: int64(len(memoryProjections)),
	}

	// Find max position
	result.MaxPosition = int64(a.globalPosition)

	// Count projections behind
	for _, p := range memoryProjections {
		if int64(p.position) < result.MaxPosition {
			result.ProjectionsBehind++
		}
	}

	if result.TotalProjections == 0 {
		result.Message = "No projections registered"
	} else if result.ProjectionsBehind > 0 {
		result.Message = strconv.FormatInt(result.ProjectionsBehind, 10) + "/" + strconv.FormatInt(result.TotalProjections, 10) + " projections behind"
	} else {
		result.Message = strconv.FormatInt(result.TotalProjections, 10) + " projections, all up to date"
	}

	return result, nil
}

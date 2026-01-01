package mink

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// ProjectionRebuilder rebuilds projections from scratch.
// It replays all events through a projection to reconstruct its read model.
type ProjectionRebuilder struct {
	store           *EventStore
	checkpointStore CheckpointStore
	logger          Logger
	metrics         ProjectionMetrics

	// Configuration
	batchSize int
}

// ProjectionRebuilderOption configures a ProjectionRebuilder.
type ProjectionRebuilderOption func(*ProjectionRebuilder)

// WithRebuilderBatchSize sets the batch size for rebuilding.
func WithRebuilderBatchSize(size int) ProjectionRebuilderOption {
	return func(r *ProjectionRebuilder) {
		r.batchSize = size
	}
}

// WithRebuilderLogger sets the logger for the rebuilder.
func WithRebuilderLogger(logger Logger) ProjectionRebuilderOption {
	return func(r *ProjectionRebuilder) {
		r.logger = logger
	}
}

// WithRebuilderMetrics sets the metrics collector for the rebuilder.
func WithRebuilderMetrics(metrics ProjectionMetrics) ProjectionRebuilderOption {
	return func(r *ProjectionRebuilder) {
		r.metrics = metrics
	}
}

// NewProjectionRebuilder creates a new projection rebuilder.
func NewProjectionRebuilder(store *EventStore, checkpointStore CheckpointStore, opts ...ProjectionRebuilderOption) *ProjectionRebuilder {
	r := &ProjectionRebuilder{
		store:           store,
		checkpointStore: checkpointStore,
		logger:          &noopLogger{},
		metrics:         &noopProjectionMetrics{},
		batchSize:       1000,
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// RebuildProgress tracks the progress of a projection rebuild.
type RebuildProgress struct {
	// ProjectionName is the name of the projection being rebuilt.
	ProjectionName string

	// TotalEvents is the total number of events to process.
	TotalEvents uint64

	// ProcessedEvents is the number of events processed so far.
	ProcessedEvents uint64

	// CurrentPosition is the current global position.
	CurrentPosition uint64

	// StartedAt is when the rebuild started.
	StartedAt time.Time

	// Duration is the elapsed time.
	Duration time.Duration

	// EventsPerSecond is the processing rate.
	EventsPerSecond float64

	// EstimatedRemaining is the estimated time remaining.
	EstimatedRemaining time.Duration

	// Completed indicates if the rebuild is complete.
	Completed bool

	// Error contains any error that occurred.
	Error error
}

// ProgressCallback is called periodically during rebuild with progress updates.
type ProgressCallback func(progress RebuildProgress)

// RebuildOptions configures a projection rebuild.
type RebuildOptions struct {
	// DeleteCheckpoint deletes the existing checkpoint before rebuilding.
	// Default: true
	DeleteCheckpoint bool

	// ClearReadModel calls the projection's Clear method before rebuilding.
	// Only applicable for projections that implement Clearable.
	// Default: true
	ClearReadModel bool

	// ProgressCallback is called periodically with progress updates.
	ProgressCallback ProgressCallback

	// ProgressInterval is how often to call the progress callback.
	// Default: 1 second
	ProgressInterval time.Duration

	// FromPosition starts rebuilding from a specific position.
	// Default: 0 (from beginning)
	FromPosition uint64

	// ToPosition stops rebuilding at a specific position.
	// Default: 0 (to end)
	ToPosition uint64
}

// DefaultRebuildOptions returns the default rebuild options.
func DefaultRebuildOptions() RebuildOptions {
	return RebuildOptions{
		DeleteCheckpoint: true,
		ClearReadModel:   true,
		ProgressInterval: time.Second,
	}
}

// Clearable is an interface for projections that can clear their read model.
type Clearable interface {
	// Clear removes all data from the read model.
	Clear(ctx context.Context) error
}

// RebuildAsync rebuilds an async projection from scratch.
func (r *ProjectionRebuilder) RebuildAsync(ctx context.Context, projection AsyncProjection, opts ...RebuildOptions) error {
	options := DefaultRebuildOptions()
	if len(opts) > 0 {
		options = opts[0]
	}

	return r.rebuild(ctx, projection.Name(), func(ctx context.Context, events []StoredEvent) error {
		return r.processAsyncBatch(ctx, projection, events)
	}, options)
}

// RebuildInline rebuilds an inline projection from scratch.
func (r *ProjectionRebuilder) RebuildInline(ctx context.Context, projection InlineProjection, opts ...RebuildOptions) error {
	options := DefaultRebuildOptions()
	if len(opts) > 0 {
		options = opts[0]
	}

	return r.rebuild(ctx, projection.Name(), func(ctx context.Context, events []StoredEvent) error {
		return r.processInlineBatch(ctx, projection, events)
	}, options)
}

// rebuild performs the actual rebuild logic.
func (r *ProjectionRebuilder) rebuild(ctx context.Context, projectionName string, processBatch func(context.Context, []StoredEvent) error, options RebuildOptions) error {
	r.logger.Info("Starting projection rebuild", "projection", projectionName)
	startTime := time.Now()

	// Delete existing checkpoint if requested
	if options.DeleteCheckpoint && r.checkpointStore != nil {
		if err := r.checkpointStore.DeleteCheckpoint(ctx, projectionName); err != nil {
			r.logger.Warn("Failed to delete checkpoint", "projection", projectionName, "error", err)
		}
	}

	// Get total events for progress tracking
	var totalEvents uint64
	adapter := r.store.Adapter()
	if pos, err := adapter.GetLastPosition(ctx); err == nil {
		if options.ToPosition > 0 && options.ToPosition < pos {
			totalEvents = options.ToPosition - options.FromPosition
		} else {
			totalEvents = pos - options.FromPosition
		}
	}

	var processedEvents uint64
	currentPosition := options.FromPosition

	// Set up progress reporting
	var progressTicker *time.Ticker
	if options.ProgressCallback != nil && options.ProgressInterval > 0 {
		progressTicker = time.NewTicker(options.ProgressInterval)
		defer progressTicker.Stop()
	}

	// Process events in batches
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check progress ticker
		if progressTicker != nil {
			select {
			case <-progressTicker.C:
				options.ProgressCallback(r.buildProgress(projectionName, totalEvents, processedEvents, currentPosition, startTime, false, nil))
			default:
			}
		}

		// Load batch of events
		events, err := r.loadEventsFromPosition(ctx, currentPosition, r.batchSize)
		if err != nil {
			return fmt.Errorf("failed to load events: %w", err)
		}

		if len(events) == 0 {
			break
		}

		// Check toPosition limit
		if options.ToPosition > 0 {
			var filteredEvents []StoredEvent
			for _, e := range events {
				if e.GlobalPosition <= options.ToPosition {
					filteredEvents = append(filteredEvents, e)
				}
			}
			events = filteredEvents
			if len(events) == 0 {
				break
			}
		}

		// Process batch
		if err := processBatch(ctx, events); err != nil {
			return fmt.Errorf("failed to process batch: %w", err)
		}

		// Update position
		lastEvent := events[len(events)-1]
		currentPosition = lastEvent.GlobalPosition + 1
		processedEvents += uint64(len(events))

		// Save checkpoint periodically
		if r.checkpointStore != nil {
			if err := r.checkpointStore.SetCheckpoint(ctx, projectionName, lastEvent.GlobalPosition); err != nil {
				r.logger.Warn("Failed to save checkpoint", "projection", projectionName, "error", err)
			}
		}

		// Check if we've reached the end
		if options.ToPosition > 0 && lastEvent.GlobalPosition >= options.ToPosition {
			break
		}
	}

	// Final progress callback
	if options.ProgressCallback != nil {
		options.ProgressCallback(r.buildProgress(projectionName, totalEvents, processedEvents, currentPosition, startTime, true, nil))
	}

	r.logger.Info("Projection rebuild completed",
		"projection", projectionName,
		"events", processedEvents,
		"duration", time.Since(startTime))

	return nil
}

// buildProgress creates a RebuildProgress struct.
func (r *ProjectionRebuilder) buildProgress(projectionName string, totalEvents, processedEvents, currentPosition uint64, startTime time.Time, completed bool, err error) RebuildProgress {
	duration := time.Since(startTime)
	var eventsPerSecond float64
	var estimatedRemaining time.Duration

	if duration.Seconds() > 0 {
		eventsPerSecond = float64(processedEvents) / duration.Seconds()
		if eventsPerSecond > 0 && totalEvents > processedEvents {
			remainingEvents := totalEvents - processedEvents
			estimatedRemaining = time.Duration(float64(remainingEvents)/eventsPerSecond) * time.Second
		}
	}

	return RebuildProgress{
		ProjectionName:     projectionName,
		TotalEvents:        totalEvents,
		ProcessedEvents:    processedEvents,
		CurrentPosition:    currentPosition,
		StartedAt:          startTime,
		Duration:           duration,
		EventsPerSecond:    eventsPerSecond,
		EstimatedRemaining: estimatedRemaining,
		Completed:          completed,
		Error:              err,
	}
}

// processAsyncBatch processes a batch using an async projection.
func (r *ProjectionRebuilder) processAsyncBatch(ctx context.Context, projection AsyncProjection, events []StoredEvent) error {
	// Filter events that this projection handles
	var filteredEvents []StoredEvent
	handledEvents := projection.HandledEvents()
	for _, event := range events {
		if len(handledEvents) == 0 || containsString(handledEvents, event.Type) {
			filteredEvents = append(filteredEvents, event)
		}
	}

	if len(filteredEvents) == 0 {
		return nil
	}

	// Try batch processing first
	err := projection.ApplyBatch(ctx, filteredEvents)
	if errors.Is(err, ErrNotImplemented) {
		// Fall back to sequential processing
		for _, event := range filteredEvents {
			if err := projection.Apply(ctx, event); err != nil {
				return err
			}
		}
		return nil
	}

	return err
}

// processInlineBatch processes a batch using an inline projection.
func (r *ProjectionRebuilder) processInlineBatch(ctx context.Context, projection InlineProjection, events []StoredEvent) error {
	handledEvents := projection.HandledEvents()
	for _, event := range events {
		if len(handledEvents) == 0 || containsString(handledEvents, event.Type) {
			if err := projection.Apply(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadEventsFromPosition loads events from a global position.
// Returns ErrSubscriptionNotSupported if the adapter does not implement SubscriptionAdapter.
func (r *ProjectionRebuilder) loadEventsFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]StoredEvent, error) {
	adapter := r.store.Adapter()

	// Check if adapter supports subscription (has LoadFromPosition)
	if subAdapter, ok := adapter.(adapters.SubscriptionAdapter); ok {
		events, err := subAdapter.LoadFromPosition(ctx, fromPosition, limit)
		if err != nil {
			return nil, err
		}
		// Convert adapters.StoredEvent to mink.StoredEvent
		result := make([]StoredEvent, len(events))
		for i, e := range events {
			result[i] = StoredEvent{
				ID:             e.ID,
				StreamID:       e.StreamID,
				Type:           e.Type,
				Data:           e.Data,
				Metadata:       Metadata(e.Metadata),
				Version:        e.Version,
				GlobalPosition: e.GlobalPosition,
				Timestamp:      e.Timestamp,
			}
		}
		return result, nil
	}

	return nil, ErrSubscriptionNotSupported
}

// containsString checks if a slice contains a string.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// ParallelRebuilder rebuilds multiple projections in parallel.
type ParallelRebuilder struct {
	rebuilder   *ProjectionRebuilder
	concurrency int
}

// NewParallelRebuilder creates a new parallel rebuilder.
func NewParallelRebuilder(rebuilder *ProjectionRebuilder, concurrency int) *ParallelRebuilder {
	if concurrency < 1 {
		concurrency = 1
	}
	return &ParallelRebuilder{
		rebuilder:   rebuilder,
		concurrency: concurrency,
	}
}

// RebuildAll rebuilds multiple async projections in parallel.
func (pr *ParallelRebuilder) RebuildAll(ctx context.Context, projections []AsyncProjection, opts ...RebuildOptions) error {
	if len(projections) == 0 {
		return nil
	}

	options := DefaultRebuildOptions()
	if len(opts) > 0 {
		options = opts[0]
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(projections))
	sem := make(chan struct{}, pr.concurrency)

	var failed atomic.Int32

	for _, projection := range projections {
		wg.Add(1)
		go func(p AsyncProjection) {
			defer wg.Done()

			// Check if already failed
			if failed.Load() > 0 {
				return
			}

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}

			if err := pr.rebuilder.RebuildAsync(ctx, p, options); err != nil {
				failed.Add(1)
				errCh <- fmt.Errorf("failed to rebuild %s: %w", p.Name(), err)
			}
		}(projection)
	}

	wg.Wait()
	close(errCh)

	// Collect errors
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errs[0] // Return first error
	}

	return nil
}

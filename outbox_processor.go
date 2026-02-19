package mink

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProcessorOption configures an OutboxProcessor.
type ProcessorOption func(*OutboxProcessor)

// WithBatchSize sets the maximum number of messages to process in a single batch.
func WithBatchSize(n int) ProcessorOption {
	return func(p *OutboxProcessor) {
		if n > 0 {
			p.batchSize = n
		}
	}
}

// WithPollInterval sets how often the processor polls for pending messages.
func WithPollInterval(d time.Duration) ProcessorOption {
	return func(p *OutboxProcessor) {
		if d > 0 {
			p.pollInterval = d
		}
	}
}

// WithMaxRetries sets the maximum number of delivery attempts.
func WithMaxRetries(n int) ProcessorOption {
	return func(p *OutboxProcessor) {
		if n > 0 {
			p.maxRetries = n
		}
	}
}

// WithRetryBackoff sets the duration between retry cycles.
func WithRetryBackoff(d time.Duration) ProcessorOption {
	return func(p *OutboxProcessor) {
		if d > 0 {
			p.retryBackoff = d
		}
	}
}

// WithCleanupInterval sets how often completed messages are cleaned up.
func WithCleanupInterval(d time.Duration) ProcessorOption {
	return func(p *OutboxProcessor) {
		if d > 0 {
			p.cleanupInterval = d
		}
	}
}

// WithCleanupAge sets the age threshold for cleaning up completed messages.
func WithCleanupAge(d time.Duration) ProcessorOption {
	return func(p *OutboxProcessor) {
		if d > 0 {
			p.cleanupAge = d
		}
	}
}

// WithPublisher registers a publisher for a given destination prefix.
func WithPublisher(publisher Publisher) ProcessorOption {
	return func(p *OutboxProcessor) {
		p.publishers[publisher.Destination()] = publisher
	}
}

// WithOutboxMetrics sets the metrics collector for the processor.
func WithOutboxMetrics(metrics OutboxMetrics) ProcessorOption {
	return func(p *OutboxProcessor) {
		p.metrics = metrics
	}
}

// WithProcessorLogger sets the logger for the processor.
func WithProcessorLogger(logger Logger) ProcessorOption {
	return func(p *OutboxProcessor) {
		p.logger = logger
	}
}

// OutboxProcessor polls the outbox store for pending messages and publishes
// them via registered publishers. It handles retries, dead-lettering, and cleanup.
type OutboxProcessor struct {
	store      OutboxStore
	publishers map[string]Publisher
	metrics    OutboxMetrics
	logger     Logger

	batchSize       int
	pollInterval    time.Duration
	maxRetries      int
	retryBackoff    time.Duration
	cleanupInterval time.Duration
	cleanupAge      time.Duration

	running  atomic.Bool
	stopping atomic.Bool
	wg       sync.WaitGroup
	stopCh   chan struct{}
}

// NewOutboxProcessor creates a new OutboxProcessor.
func NewOutboxProcessor(store OutboxStore, opts ...ProcessorOption) *OutboxProcessor {
	p := &OutboxProcessor{
		store:           store,
		publishers:      make(map[string]Publisher),
		metrics:         &noopOutboxMetrics{},
		logger:          &noopLogger{},
		batchSize:       100,
		pollInterval:    time.Second,
		maxRetries:      5,
		retryBackoff:    5 * time.Second,
		cleanupInterval: time.Hour,
		cleanupAge:      7 * 24 * time.Hour,
		stopCh:          make(chan struct{}),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Start begins the background processing loop.
func (p *OutboxProcessor) Start(ctx context.Context) error {
	if p.running.Load() {
		return ErrOutboxProcessorRunning
	}

	p.running.Store(true)
	p.stopping.Store(false)
	p.stopCh = make(chan struct{})

	p.wg.Add(1)
	go p.processLoop(ctx)

	p.wg.Add(1)
	go p.maintenanceLoop(ctx)

	p.logger.Info("Outbox processor started")
	return nil
}

// Stop gracefully stops the processor, draining in-flight work.
func (p *OutboxProcessor) Stop(ctx context.Context) error {
	if !p.running.Load() {
		return nil
	}

	p.stopping.Store(true)
	close(p.stopCh)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.running.Store(false)
		p.logger.Info("Outbox processor stopped")
		return nil
	case <-ctx.Done():
		p.running.Store(false)
		return ctx.Err()
	}
}

// IsRunning returns true if the processor is running.
func (p *OutboxProcessor) IsRunning() bool {
	return p.running.Load()
}

// processLoop polls for and processes pending outbox messages.
func (p *OutboxProcessor) processLoop(ctx context.Context) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.processBatch(ctx); err != nil {
				if p.stopping.Load() {
					return
				}
				p.logger.Error("Outbox batch processing error", "error", err)
			}
		}
	}
}

// maintenanceLoop runs periodic maintenance tasks (retry, dead-letter, cleanup).
func (p *OutboxProcessor) maintenanceLoop(ctx context.Context) {
	defer p.wg.Done()

	retryTicker := time.NewTicker(p.retryBackoff)
	defer retryTicker.Stop()

	cleanupTicker := time.NewTicker(p.cleanupInterval)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ctx.Done():
			return
		case <-retryTicker.C:
			p.runMaintenance(ctx)
		case <-cleanupTicker.C:
			p.runCleanup(ctx)
		}
	}
}

// processBatch fetches and processes a batch of pending messages.
func (p *OutboxProcessor) processBatch(ctx context.Context) error {
	start := time.Now()

	messages, err := p.store.FetchPending(ctx, p.batchSize)
	if err != nil {
		return fmt.Errorf("failed to fetch pending messages: %w", err)
	}

	if len(messages) == 0 {
		return nil
	}

	// Group messages by destination prefix
	grouped := make(map[string][]*OutboxMessage)
	for _, msg := range messages {
		prefix := destinationPrefix(msg.Destination)
		grouped[prefix] = append(grouped[prefix], msg)
	}

	// Publish each group
	for prefix, msgs := range grouped {
		publisher, ok := p.publishers[prefix]
		if !ok {
			// No publisher for this destination prefix; mark as failed
			for _, msg := range msgs {
				p.logger.Error("No publisher for destination", "destination", msg.Destination, "prefix", prefix)
				if err := p.store.MarkFailed(ctx, msg.ID, fmt.Errorf("%w: %s", ErrPublisherNotFound, prefix)); err != nil {
					p.logger.Error("Failed to mark message as failed", "id", msg.ID, "error", err)
				}
				p.metrics.RecordMessageFailed(msg.Destination)
			}
			continue
		}

		if err := publisher.Publish(ctx, msgs); err != nil {
			// Mark all messages in this group as failed
			for _, msg := range msgs {
				if markErr := p.store.MarkFailed(ctx, msg.ID, err); markErr != nil {
					p.logger.Error("Failed to mark message as failed", "id", msg.ID, "error", markErr)
				}
				p.metrics.RecordMessageProcessed(msg.Destination, false)
				p.metrics.RecordMessageFailed(msg.Destination)
			}
		} else {
			// Mark all messages as completed
			ids := make([]string, len(msgs))
			for i, msg := range msgs {
				ids[i] = msg.ID
				p.metrics.RecordMessageProcessed(msg.Destination, true)
			}
			if err := p.store.MarkCompleted(ctx, ids); err != nil {
				p.logger.Error("Failed to mark messages as completed", "error", err)
			}
		}
	}

	p.metrics.RecordBatchDuration(time.Since(start))
	return nil
}

// runMaintenance performs retry and dead-letter operations.
func (p *OutboxProcessor) runMaintenance(ctx context.Context) {
	// Retry failed messages that haven't exhausted retries
	retried, err := p.store.RetryFailed(ctx, p.maxRetries)
	if err != nil {
		p.logger.Error("Failed to retry failed messages", "error", err)
	} else if retried > 0 {
		p.logger.Info("Retried failed outbox messages", "count", retried)
	}

	// Move exhausted messages to dead letter
	deadLettered, err := p.store.MoveToDeadLetter(ctx, p.maxRetries)
	if err != nil {
		p.logger.Error("Failed to move messages to dead letter", "error", err)
	} else if deadLettered > 0 {
		p.logger.Warn("Moved outbox messages to dead letter", "count", deadLettered)
		for i := int64(0); i < deadLettered; i++ {
			p.metrics.RecordMessageDeadLettered()
		}
	}
}

// runCleanup removes old completed messages.
func (p *OutboxProcessor) runCleanup(ctx context.Context) {
	cleaned, err := p.store.Cleanup(ctx, p.cleanupAge)
	if err != nil {
		p.logger.Error("Failed to cleanup completed messages", "error", err)
	} else if cleaned > 0 {
		p.logger.Info("Cleaned up completed outbox messages", "count", cleaned)
	}
}

// destinationPrefix extracts the prefix from a destination string.
// For example, "webhook:https://example.com" returns "webhook".
func destinationPrefix(destination string) string {
	if idx := strings.Index(destination, ":"); idx > 0 {
		return destination[:idx]
	}
	return destination
}

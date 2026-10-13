package mink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
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

// WithProcessingTimeout sets how long a message may remain claimed (in
// OutboxProcessing) before the maintenance sweep reclaims it back to pending.
// This recovers messages orphaned by a processor crash. Default: 5 minutes.
func WithProcessingTimeout(d time.Duration) ProcessorOption {
	return func(p *OutboxProcessor) {
		if d > 0 {
			p.processingTimeout = d
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

	batchSize         int
	pollInterval      time.Duration
	maxRetries        int
	retryBackoff      time.Duration
	cleanupInterval   time.Duration
	cleanupAge        time.Duration
	processingTimeout time.Duration

	running  atomic.Bool
	stopping atomic.Bool
	wg       sync.WaitGroup
	stopCh   chan struct{}
}

// NewOutboxProcessor creates a new OutboxProcessor.
func NewOutboxProcessor(store OutboxStore, opts ...ProcessorOption) *OutboxProcessor {
	p := &OutboxProcessor{
		store:             store,
		publishers:        make(map[string]Publisher),
		metrics:           &noopOutboxMetrics{},
		logger:            &noopLogger{},
		batchSize:         100,
		pollInterval:      time.Second,
		maxRetries:        5,
		retryBackoff:      5 * time.Second,
		cleanupInterval:   time.Hour,
		cleanupAge:        7 * 24 * time.Hour,
		processingTimeout: 5 * time.Minute,
		stopCh:            make(chan struct{}),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Start begins the background processing loop.
func (p *OutboxProcessor) Start(ctx context.Context) error {
	if !p.running.CompareAndSwap(false, true) {
		return ErrOutboxProcessorRunning
	}

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

	if !p.stopping.CompareAndSwap(false, true) {
		return nil
	}
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

	p.metrics.RecordPendingMessages(int64(len(messages)))

	if len(messages) == 0 {
		return nil
	}

	// Group messages by destination prefix
	messagesByPrefix := make(map[string][]*OutboxMessage)
	for _, msg := range messages {
		prefix := destinationPrefix(msg.Destination)
		messagesByPrefix[prefix] = append(messagesByPrefix[prefix], msg)
	}

	// Publish each group
	for prefix, msgs := range messagesByPrefix {
		publisher, ok := p.publishers[prefix]
		if !ok {
			// No publisher for this destination prefix; mark as failed. The
			// destination is stored data nobody registered, so nothing about its
			// format is known: a poisoned row may carry a credential in its prefix
			// or as its bare value, where redactDestination (which only understands
			// URLs) would echo it. Neither the prefix nor the value therefore reaches
			// the log or the persisted last_error — only a one-way digest and the
			// length, enough to match the row in the outbox for diagnosis. The prefix
			// is likewise NOT a bounded label value: the failure is recorded under
			// the fixed UnregisteredDestinationLabel.
			for _, msg := range msgs {
				digest := destinationDigest(msg.Destination)
				p.logger.Error("No publisher for destination",
					"id", msg.ID, "destination_digest", digest, "destination_length", len(msg.Destination))
				if err := p.store.MarkFailed(ctx, msg.ID, fmt.Errorf("%w: destination digest %s", ErrPublisherNotFound, digest)); err != nil {
					p.logger.Error("Failed to mark message as failed", "id", msg.ID, "error", err)
				}
				p.metrics.RecordMessageFailed(UnregisteredDestinationLabel)
			}
			continue
		}

		if err := publisher.Publish(ctx, msgs); err != nil {
			// Mark all messages in this group as failed
			for _, msg := range msgs {
				if markErr := p.store.MarkFailed(ctx, msg.ID, err); markErr != nil {
					p.logger.Error("Failed to mark message as failed", "id", msg.ID, "error", markErr)
				}
				p.metrics.RecordMessageProcessed(prefix, false)
				p.metrics.RecordMessageFailed(prefix)
			}
		} else {
			// Mark all messages as completed
			ids := make([]string, len(msgs))
			for i, msg := range msgs {
				ids[i] = msg.ID
				p.metrics.RecordMessageProcessed(prefix, true)
			}
			if err := p.store.MarkCompleted(ctx, ids); err != nil {
				p.logger.Error("Failed to mark messages as completed", "error", err)
				return fmt.Errorf("failed to mark messages as completed: %w", err)
			}
		}
	}

	p.metrics.RecordBatchDuration(time.Since(start))
	return nil
}

// runMaintenance performs reclaim, retry, and dead-letter operations.
func (p *OutboxProcessor) runMaintenance(ctx context.Context) {
	// Reclaim messages orphaned in OutboxProcessing by a crashed processor.
	if p.processingTimeout > 0 {
		reclaimed, err := p.store.ReclaimStale(ctx, p.processingTimeout)
		if err != nil {
			p.logger.Error("Failed to reclaim stale outbox messages", "error", err)
		} else if reclaimed > 0 {
			p.logger.Warn("Reclaimed stale outbox messages", "count", reclaimed)
		}
	}

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

// UnregisteredDestinationLabel is the destination label under which the
// OutboxProcessor reports, via OutboxMetrics.RecordMessageFailed, a message
// whose destination prefix has no registered Publisher.
//
// A destination is stored data, so an unregistered prefix is whatever was
// written to the outbox ("tenant-123:https://…", a typo, a decommissioned
// publisher) and is unbounded; recording it verbatim would let every distinct
// stored value create a new metric series. Collapsing all such failures onto
// this one fixed value keeps the "destination" label set bounded by the
// registered publishers plus this label. The "No publisher for destination"
// log line identifies the offending row by message id, destination digest
// (see destinationDigest) and destination length — never by the prefix or the
// value, whose format is unknown and may embed a credential. Avoid registering
// a Publisher whose Destination() is this value, or its failures would merge
// with the unroutable ones.
const UnregisteredDestinationLabel = "unregistered"

// destinationDigestHexLen is the number of hex digits destinationDigest keeps:
// 48 bits, ample to tell outbox rows apart for diagnosis and useless for
// recovering the destination.
const destinationDigestHexLen = 12

// destinationDigest returns a short one-way fingerprint of an outbox
// destination: the first destinationDigestHexLen lowercase hex digits of its
// SHA-256. The processor uses it in place of the destination wherever a
// destination with NO registered publisher must be reported (the "No publisher
// for destination" log line and the ErrPublisherNotFound persisted as the
// message's last error). Such a destination is stored data whose format nobody
// registered, so even its prefix may be a credential; the digest lets an
// operator match the report to the row (sha256 of the stored value) without the
// value, or any part of it, ever leaving the store. Destinations that DO route
// to a registered publisher keep redactDestination: the application registered
// that prefix, so its format is known.
func destinationDigest(dest string) string {
	sum := sha256.Sum256([]byte(dest))
	return hex.EncodeToString(sum[:])[:destinationDigestHexLen]
}

// destinationPrefix extracts the prefix from a destination string.
// For example, "webhook:https://example.com" returns "webhook".
func destinationPrefix(destination string) string {
	if idx := strings.Index(destination, ":"); idx > 0 {
		return destination[:idx]
	}
	return destination
}

// redactDestination returns a log-safe rendering of an outbox destination
// whose prefix the application registered (so its format is known).
//
// URL-shaped destinations such as "webhook:https://user:token@host/hook?key=…"
// may embed credentials in the URL's userinfo or query string — and the most
// common webhook providers put the bearer secret in the PATH
// ("https://hooks.slack.com/services/T0/B0/<secret>",
// "https://discord.com/api/webhooks/<id>/<token>") — while routing failures
// are logged with the destination. For a destination that contains a URL only
// its origin, "<prefix>:scheme://host" (host includes the port when one was
// given), is kept: userinfo, path, query and fragment are all dropped. A
// destination that does not contain a URL ("kafka:topic", "sns:arn:aws:sns:…")
// is returned unchanged, and a URL that cannot be parsed is replaced by
// "<prefix>:<redacted>" rather than echoed. Because a non-URL destination
// passes through verbatim, this is NOT used for a destination with no
// registered publisher — nothing is known about such a value, so the
// processor reports it only by destinationDigest.
func redactDestination(dest string) string {
	i := strings.Index(dest, "://")
	if i < 0 {
		return dest
	}
	// Walk back over the scheme ([A-Za-z0-9+.-]) to find where the URL starts;
	// whatever precedes it is the publisher prefix (e.g. "webhook:").
	start := i
	for start > 0 && isSchemeByte(dest[start-1]) {
		start--
	}
	head, rawURL := dest[:start], dest[start:]

	u, err := url.Parse(rawURL)
	if err != nil {
		return head + "<redacted>"
	}
	origin := url.URL{Scheme: u.Scheme, Host: u.Host}
	return head + origin.String()
}

// isSchemeByte reports whether c may appear in a URL scheme.
func isSchemeByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '+' || c == '-' || c == '.'
}

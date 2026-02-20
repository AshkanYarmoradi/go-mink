package mink

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPublisher is a test publisher that records published messages.
type mockPublisher struct {
	mu         sync.Mutex
	published  []*adapters.OutboxMessage
	publishErr error
	dest       string
}

func newMockPublisher(dest string) *mockPublisher {
	return &mockPublisher{dest: dest}
}

func (m *mockPublisher) Destination() string {
	return m.dest
}

func (m *mockPublisher) Publish(ctx context.Context, messages []*adapters.OutboxMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.publishErr != nil {
		return m.publishErr
	}
	m.published = append(m.published, messages...)
	return nil
}

func (m *mockPublisher) getPublished() []*adapters.OutboxMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*adapters.OutboxMessage, len(m.published))
	copy(result, m.published)
	return result
}

func TestOutboxProcessor_StartStop(t *testing.T) {
	store := memory.NewOutboxStore()
	processor := NewOutboxProcessor(store,
		WithPollInterval(50*time.Millisecond),
	)

	ctx := context.Background()

	// Start
	err := processor.Start(ctx)
	require.NoError(t, err)
	assert.True(t, processor.IsRunning())

	// Starting again should fail
	err = processor.Start(ctx)
	assert.ErrorIs(t, err, ErrOutboxProcessorRunning)

	// Stop
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = processor.Stop(stopCtx)
	require.NoError(t, err)
	assert.False(t, processor.IsRunning())
}

func TestOutboxProcessor_ProcessMessages(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	// Schedule messages
	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "test:endpoint1",
			Payload:     []byte(`{"id":"1"}`),
		},
		{
			AggregateID: "order-2",
			EventType:   "OrderShipped",
			Destination: "test:endpoint2",
			Payload:     []byte(`{"id":"2"}`),
		},
	})
	require.NoError(t, err)

	pub := newMockPublisher("test")

	processor := NewOutboxProcessor(store,
		WithPublisher(pub),
		WithPollInterval(50*time.Millisecond),
	)

	err = processor.Start(ctx)
	require.NoError(t, err)

	// Wait for processing
	assert.Eventually(t, func() bool {
		return len(pub.getPublished()) == 2
	}, 5*time.Second, 50*time.Millisecond)

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = processor.Stop(stopCtx)
	require.NoError(t, err)

	// Verify messages were completed
	counts := store.CountByStatus()
	assert.Equal(t, 2, counts[adapters.OutboxCompleted])
}

func TestOutboxProcessor_PublishError(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "test:endpoint1",
			Payload:     []byte(`{"id":"1"}`),
		},
	})
	require.NoError(t, err)

	pub := newMockPublisher("test")
	pub.publishErr = errors.New("connection refused")

	processor := NewOutboxProcessor(store,
		WithPublisher(pub),
		WithPollInterval(50*time.Millisecond),
		WithRetryBackoff(100*time.Millisecond),
	)

	err = processor.Start(ctx)
	require.NoError(t, err)

	// Wait for processing attempt
	assert.Eventually(t, func() bool {
		counts := store.CountByStatus()
		return counts[adapters.OutboxFailed] > 0
	}, 5*time.Second, 50*time.Millisecond)

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = processor.Stop(stopCtx)
	require.NoError(t, err)
}

func TestOutboxProcessor_NoPublisherForDestination(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "unknown:endpoint",
			Payload:     []byte(`{"id":"1"}`),
		},
	})
	require.NoError(t, err)

	// No publisher registered for "unknown" prefix
	processor := NewOutboxProcessor(store,
		WithPollInterval(50*time.Millisecond),
	)

	err = processor.Start(ctx)
	require.NoError(t, err)

	// Wait for message to be marked as failed
	assert.Eventually(t, func() bool {
		counts := store.CountByStatus()
		return counts[adapters.OutboxFailed] > 0
	}, 5*time.Second, 50*time.Millisecond)

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = processor.Stop(stopCtx)
	require.NoError(t, err)
}

func TestOutboxProcessor_DeadLetter(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "test:endpoint",
			Payload:     []byte(`{"id":"1"}`),
			MaxAttempts: 1,
		},
	})
	require.NoError(t, err)

	pub := newMockPublisher("test")
	pub.publishErr = errors.New("always fails")

	processor := NewOutboxProcessor(store,
		WithPublisher(pub),
		WithPollInterval(50*time.Millisecond),
		WithRetryBackoff(100*time.Millisecond),
		WithMaxRetries(1),
	)

	err = processor.Start(ctx)
	require.NoError(t, err)

	// Wait for dead-letter
	assert.Eventually(t, func() bool {
		counts := store.CountByStatus()
		return counts[adapters.OutboxDeadLetter] > 0
	}, 10*time.Second, 100*time.Millisecond)

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = processor.Stop(stopCtx)
	require.NoError(t, err)
}

func TestOutboxProcessor_Options(t *testing.T) {
	store := memory.NewOutboxStore()

	processor := NewOutboxProcessor(store,
		WithBatchSize(50),
		WithPollInterval(2*time.Second),
		WithMaxRetries(10),
		WithRetryBackoff(30*time.Second),
		WithCleanupInterval(2*time.Hour),
		WithCleanupAge(14*24*time.Hour),
	)

	assert.Equal(t, 50, processor.batchSize)
	assert.Equal(t, 2*time.Second, processor.pollInterval)
	assert.Equal(t, 10, processor.maxRetries)
	assert.Equal(t, 30*time.Second, processor.retryBackoff)
	assert.Equal(t, 2*time.Hour, processor.cleanupInterval)
	assert.Equal(t, 14*24*time.Hour, processor.cleanupAge)
}

func TestDestinationPrefix(t *testing.T) {
	tests := []struct {
		destination string
		want        string
	}{
		{"webhook:https://example.com", "webhook"},
		{"kafka:orders", "kafka"},
		{"sns:arn:aws:sns:us-east-1:123:topic", "sns"},
		{"noprefix", "noprefix"},
	}

	for _, tt := range tests {
		t.Run(tt.destination, func(t *testing.T) {
			got := destinationPrefix(tt.destination)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOutboxProcessor_StopWithoutStart(t *testing.T) {
	store := memory.NewOutboxStore()
	processor := NewOutboxProcessor(store)

	ctx := context.Background()
	err := processor.Stop(ctx)
	assert.NoError(t, err)
}

// =============================================================================
// New tests: runCleanup
// =============================================================================

func TestOutboxProcessor_Cleanup(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	// Schedule and complete a message
	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "test:endpoint",
			Payload:     []byte(`{}`),
		},
	})
	require.NoError(t, err)

	fetched, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, fetched, 1)

	err = store.MarkCompleted(ctx, []string{fetched[0].ID})
	require.NoError(t, err)

	pub := newMockPublisher("test")

	processor := NewOutboxProcessor(store,
		WithPublisher(pub),
		WithPollInterval(50*time.Millisecond),
		WithCleanupInterval(50*time.Millisecond),
		WithCleanupAge(1*time.Millisecond), // Nearly immediate cleanup
	)

	// Small delay so completed message is older than cleanupAge
	time.Sleep(10 * time.Millisecond)

	err = processor.Start(ctx)
	require.NoError(t, err)

	// Wait for cleanup to run
	assert.Eventually(t, func() bool {
		return store.Count() == 0
	}, 5*time.Second, 50*time.Millisecond)

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = processor.Stop(stopCtx)
	require.NoError(t, err)
}

func TestOutboxProcessor_Cleanup_StoreError(t *testing.T) {
	logger := &recordingLogger{}

	processor := &OutboxProcessor{
		store:      &failingCleanupStore{cleanupErr: errors.New("cleanup failed")},
		publishers: make(map[string]Publisher),
		metrics:    &noopOutboxMetrics{},
		logger:     logger,
		cleanupAge: time.Hour,
	}

	processor.runCleanup(context.Background())

	assert.NotEmpty(t, logger.errors)
	assert.Contains(t, logger.errors[0], "Failed to cleanup completed messages")
}

// failingCleanupStore wraps memory.OutboxStore with a failing Cleanup method.
type failingCleanupStore struct {
	memory.OutboxStore
	cleanupErr error
}

func (s *failingCleanupStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, s.cleanupErr
}

// =============================================================================
// New tests: metrics/logger options
// =============================================================================

// mockOutboxMetrics records metric calls.
type mockOutboxMetrics struct {
	mu                   sync.Mutex
	processedCalls       []string // destination
	processedSuccessCnt  int
	processedFailureCnt  int
	failedCalls          []string
	deadLetteredCnt      int
	batchDurations       []time.Duration
	pendingMessagesCalls []int64
}

func (m *mockOutboxMetrics) RecordMessageProcessed(destination string, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processedCalls = append(m.processedCalls, destination)
	if success {
		m.processedSuccessCnt++
	} else {
		m.processedFailureCnt++
	}
}

func (m *mockOutboxMetrics) RecordMessageFailed(destination string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failedCalls = append(m.failedCalls, destination)
}

func (m *mockOutboxMetrics) RecordMessageDeadLettered() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deadLetteredCnt++
}

func (m *mockOutboxMetrics) RecordBatchDuration(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchDurations = append(m.batchDurations, d)
}

func (m *mockOutboxMetrics) RecordPendingMessages(count int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingMessagesCalls = append(m.pendingMessagesCalls, count)
}

func TestOutboxProcessor_WithOutboxMetrics(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "test:endpoint",
			Payload:     []byte(`{}`),
		},
	})
	require.NoError(t, err)

	pub := newMockPublisher("test")
	metrics := &mockOutboxMetrics{}

	processor := NewOutboxProcessor(store,
		WithPublisher(pub),
		WithPollInterval(50*time.Millisecond),
		WithOutboxMetrics(metrics),
	)

	err = processor.Start(ctx)
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return len(pub.getPublished()) == 1
	}, 5*time.Second, 50*time.Millisecond)

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = processor.Stop(stopCtx)
	require.NoError(t, err)

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	assert.Equal(t, 1, metrics.processedSuccessCnt)
	assert.NotEmpty(t, metrics.batchDurations)
}

func TestOutboxProcessor_MetricsOnFailure(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "test:endpoint",
			Payload:     []byte(`{}`),
		},
	})
	require.NoError(t, err)

	pub := newMockPublisher("test")
	pub.publishErr = errors.New("publish failed")
	metrics := &mockOutboxMetrics{}

	processor := NewOutboxProcessor(store,
		WithPublisher(pub),
		WithPollInterval(50*time.Millisecond),
		WithOutboxMetrics(metrics),
	)

	err = processor.Start(ctx)
	require.NoError(t, err)

	// Wait for failure to be recorded
	assert.Eventually(t, func() bool {
		metrics.mu.Lock()
		defer metrics.mu.Unlock()
		return len(metrics.failedCalls) > 0
	}, 5*time.Second, 50*time.Millisecond)

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = processor.Stop(stopCtx)
	require.NoError(t, err)

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	assert.NotEmpty(t, metrics.failedCalls)
	assert.Equal(t, 1, metrics.processedFailureCnt)
}

func TestOutboxProcessor_WithProcessorLogger(t *testing.T) {
	store := memory.NewOutboxStore()
	logger := &recordingLogger{}

	processor := NewOutboxProcessor(store,
		WithPollInterval(50*time.Millisecond),
		WithProcessorLogger(logger),
	)

	ctx := context.Background()
	err := processor.Start(ctx)
	require.NoError(t, err)

	// Logger should have recorded "started"
	assert.Contains(t, logger.infos, "Outbox processor started")

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = processor.Stop(stopCtx)
	require.NoError(t, err)

	assert.Contains(t, logger.infos, "Outbox processor stopped")
}

// =============================================================================
// New tests: processBatch error paths
// =============================================================================

func TestOutboxProcessor_MultiplePublishers(t *testing.T) {
	store := memory.NewOutboxStore()
	ctx := context.Background()

	err := store.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "webhook:https://example.com",
			Payload:     []byte(`{"id":"1"}`),
		},
		{
			AggregateID: "order-2",
			EventType:   "OrderShipped",
			Destination: "kafka:orders",
			Payload:     []byte(`{"id":"2"}`),
		},
	})
	require.NoError(t, err)

	webhookPub := newMockPublisher("webhook")
	kafkaPub := newMockPublisher("kafka")

	processor := NewOutboxProcessor(store,
		WithPublisher(webhookPub),
		WithPublisher(kafkaPub),
		WithPollInterval(50*time.Millisecond),
	)

	err = processor.Start(ctx)
	require.NoError(t, err)

	// Wait for both publishers to receive messages
	assert.Eventually(t, func() bool {
		return len(webhookPub.getPublished()) == 1 && len(kafkaPub.getPublished()) == 1
	}, 5*time.Second, 50*time.Millisecond)

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = processor.Stop(stopCtx)
	require.NoError(t, err)

	// Verify correct routing
	assert.Equal(t, "webhook:https://example.com", webhookPub.getPublished()[0].Destination)
	assert.Equal(t, "kafka:orders", kafkaPub.getPublished()[0].Destination)
}

// =============================================================================
// New tests: guard clauses (zero-value options)
// =============================================================================

func TestOutboxProcessor_Options_ZeroValues(t *testing.T) {
	store := memory.NewOutboxStore()

	// Create with zero values — should keep defaults
	processor := NewOutboxProcessor(store,
		WithBatchSize(0),
		WithPollInterval(0),
		WithMaxRetries(0),
		WithRetryBackoff(0),
		WithCleanupInterval(0),
		WithCleanupAge(0),
	)

	// Verify defaults are preserved
	assert.Equal(t, 100, processor.batchSize)
	assert.Equal(t, time.Second, processor.pollInterval)
	assert.Equal(t, 5, processor.maxRetries)
	assert.Equal(t, 5*time.Second, processor.retryBackoff)
	assert.Equal(t, time.Hour, processor.cleanupInterval)
	assert.Equal(t, 7*24*time.Hour, processor.cleanupAge)
}

func TestOutboxProcessor_Options_NegativeValues(t *testing.T) {
	store := memory.NewOutboxStore()

	processor := NewOutboxProcessor(store,
		WithBatchSize(-1),
		WithPollInterval(-time.Second),
		WithMaxRetries(-1),
		WithRetryBackoff(-time.Second),
		WithCleanupInterval(-time.Second),
		WithCleanupAge(-time.Second),
	)

	// Verify defaults are preserved
	assert.Equal(t, 100, processor.batchSize)
	assert.Equal(t, time.Second, processor.pollInterval)
	assert.Equal(t, 5, processor.maxRetries)
	assert.Equal(t, 5*time.Second, processor.retryBackoff)
	assert.Equal(t, time.Hour, processor.cleanupInterval)
	assert.Equal(t, 7*24*time.Hour, processor.cleanupAge)
}

// =============================================================================
// processBatch direct tests
// =============================================================================

func TestOutboxProcessor_ProcessBatch_MarkCompletedError(t *testing.T) {
	logger := &recordingLogger{}
	store := &failingMarkStore{
		OutboxStore:      memory.NewOutboxStore(),
		markCompletedErr: errors.New("mark completed failed"),
	}
	ctx := context.Background()

	err := store.OutboxStore.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "test:endpoint",
			Payload:     []byte(`{}`),
		},
	})
	require.NoError(t, err)

	pub := newMockPublisher("test")

	processor := NewOutboxProcessor(store,
		WithPublisher(pub),
		WithProcessorLogger(logger),
	)

	// Call processBatch directly - MarkCompleted errors are now propagated
	err = processor.processBatch(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mark completed failed")

	assert.NotEmpty(t, logger.errors)
	assert.Contains(t, logger.errors[0], "Failed to mark messages as completed")
}

// failingMarkStore wraps memory.OutboxStore with failing mark methods.
type failingMarkStore struct {
	*memory.OutboxStore
	markCompletedErr error
	markFailedErr    error
}

func (s *failingMarkStore) MarkCompleted(ctx context.Context, ids []string) error {
	if s.markCompletedErr != nil {
		return s.markCompletedErr
	}
	return s.OutboxStore.MarkCompleted(ctx, ids)
}

func (s *failingMarkStore) MarkFailed(ctx context.Context, id string, lastErr error) error {
	if s.markFailedErr != nil {
		return s.markFailedErr
	}
	return s.OutboxStore.MarkFailed(ctx, id, lastErr)
}

func TestOutboxProcessor_ProcessBatch_MarkFailedError(t *testing.T) {
	logger := &recordingLogger{}
	store := &failingMarkStore{
		OutboxStore:   memory.NewOutboxStore(),
		markFailedErr: errors.New("mark failed error"),
	}
	ctx := context.Background()

	err := store.OutboxStore.Schedule(ctx, []*adapters.OutboxMessage{
		{
			AggregateID: "order-1",
			EventType:   "OrderCreated",
			Destination: "test:endpoint",
			Payload:     []byte(`{}`),
		},
	})
	require.NoError(t, err)

	pub := newMockPublisher("test")
	pub.publishErr = errors.New("publish failed")

	processor := NewOutboxProcessor(store,
		WithPublisher(pub),
		WithProcessorLogger(logger),
	)

	err = processor.processBatch(ctx)
	require.NoError(t, err)

	assert.NotEmpty(t, logger.errors)
	assert.Contains(t, logger.errors[0], "Failed to mark message as failed")
}

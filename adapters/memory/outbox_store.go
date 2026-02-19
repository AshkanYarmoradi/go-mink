package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/google/uuid"
)

// Ensure interface compliance at compile time
var _ adapters.OutboxStore = (*OutboxStore)(nil)

// OutboxStore provides an in-memory implementation of adapters.OutboxStore.
// This is primarily intended for testing and development purposes.
// Note: ScheduleInTx ignores the tx parameter since in-memory storage has no real transactions.
type OutboxStore struct {
	mu       sync.RWMutex
	messages map[string]*adapters.OutboxMessage
}

// NewOutboxStore creates a new in-memory OutboxStore.
func NewOutboxStore() *OutboxStore {
	return &OutboxStore{
		messages: make(map[string]*adapters.OutboxMessage),
	}
}

// Schedule stores outbox messages for later processing.
func (s *OutboxStore) Schedule(ctx context.Context, messages []*adapters.OutboxMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, msg := range messages {
		if msg.ID == "" {
			msg.ID = uuid.New().String()
		}
		if msg.CreatedAt.IsZero() {
			msg.CreatedAt = now
		}
		if msg.ScheduledAt.IsZero() {
			msg.ScheduledAt = now
		}
		if msg.MaxAttempts == 0 {
			msg.MaxAttempts = 5
		}
		msg.Status = adapters.OutboxPending

		copied := s.copyMessage(msg)
		s.messages[copied.ID] = copied
	}

	return nil
}

// ScheduleInTx stores outbox messages (tx parameter is ignored for in-memory store).
func (s *OutboxStore) ScheduleInTx(ctx context.Context, _ interface{}, messages []*adapters.OutboxMessage) error {
	return s.Schedule(ctx, messages)
}

// FetchPending atomically claims up to limit pending messages for processing.
func (s *OutboxStore) FetchPending(ctx context.Context, limit int) ([]*adapters.OutboxMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Collect pending messages sorted by scheduled_at
	var pending []*adapters.OutboxMessage
	for _, msg := range s.messages {
		if msg.Status == adapters.OutboxPending && !msg.ScheduledAt.After(now) {
			pending = append(pending, msg)
		}
	}

	sort.Slice(pending, func(i, j int) bool {
		return pending[i].ScheduledAt.Before(pending[j].ScheduledAt)
	})

	if limit > 0 && len(pending) > limit {
		pending = pending[:limit]
	}

	// Claim messages by setting status to Processing
	result := make([]*adapters.OutboxMessage, len(pending))
	for i, msg := range pending {
		msg.Status = adapters.OutboxProcessing
		msg.Attempts++
		msg.LastAttemptAt = &now
		result[i] = s.copyMessage(msg)
	}

	return result, nil
}

// MarkCompleted marks messages as successfully delivered.
func (s *OutboxStore) MarkCompleted(ctx context.Context, ids []string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, id := range ids {
		msg, exists := s.messages[id]
		if !exists {
			continue
		}
		msg.Status = adapters.OutboxCompleted
		msg.ProcessedAt = &now
	}

	return nil
}

// MarkFailed marks a message as failed with an error description.
func (s *OutboxStore) MarkFailed(ctx context.Context, id string, lastErr error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	msg, exists := s.messages[id]
	if !exists {
		return adapters.ErrOutboxMessageNotFound
	}

	msg.Status = adapters.OutboxFailed
	if lastErr != nil {
		msg.LastError = lastErr.Error()
	}

	return nil
}

// RetryFailed resets eligible failed messages (below maxAttempts) to pending.
func (s *OutboxStore) RetryFailed(ctx context.Context, maxAttempts int) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var count int64
	for _, msg := range s.messages {
		if msg.Status == adapters.OutboxFailed && msg.Attempts < maxAttempts {
			msg.Status = adapters.OutboxPending
			count++
		}
	}

	return count, nil
}

// MoveToDeadLetter transitions messages that exceeded maxAttempts to dead letter.
func (s *OutboxStore) MoveToDeadLetter(ctx context.Context, maxAttempts int) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var count int64
	for _, msg := range s.messages {
		if msg.Status == adapters.OutboxFailed && msg.Attempts >= maxAttempts {
			msg.Status = adapters.OutboxDeadLetter
			count++
		}
	}

	return count, nil
}

// GetDeadLetterMessages retrieves dead-lettered messages.
func (s *OutboxStore) GetDeadLetterMessages(ctx context.Context, limit int) ([]*adapters.OutboxMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*adapters.OutboxMessage
	for _, msg := range s.messages {
		if msg.Status == adapters.OutboxDeadLetter {
			result = append(result, s.copyMessage(msg))
			if limit > 0 && len(result) >= limit {
				break
			}
		}
	}

	return result, nil
}

// Cleanup removes old completed messages.
func (s *OutboxStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	var count int64
	for id, msg := range s.messages {
		if msg.Status == adapters.OutboxCompleted && msg.ProcessedAt != nil && msg.ProcessedAt.Before(cutoff) {
			delete(s.messages, id)
			count++
		}
	}

	return count, nil
}

// Initialize is a no-op for the in-memory store.
func (s *OutboxStore) Initialize(ctx context.Context) error {
	return nil
}

// Close is a no-op for the in-memory store.
func (s *OutboxStore) Close() error {
	return nil
}

// Clear removes all messages (useful for testing).
func (s *OutboxStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make(map[string]*adapters.OutboxMessage)
}

// Count returns the total number of messages stored.
func (s *OutboxStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

// CountByStatus returns the count of messages by status.
func (s *OutboxStore) CountByStatus() map[adapters.OutboxStatus]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	counts := make(map[adapters.OutboxStatus]int)
	for _, msg := range s.messages {
		counts[msg.Status]++
	}
	return counts
}

// copyMessage creates a deep copy of an OutboxMessage.
func (s *OutboxStore) copyMessage(msg *adapters.OutboxMessage) *adapters.OutboxMessage {
	copied := &adapters.OutboxMessage{
		ID:          msg.ID,
		AggregateID: msg.AggregateID,
		EventType:   msg.EventType,
		Destination: msg.Destination,
		Status:      msg.Status,
		Attempts:    msg.Attempts,
		MaxAttempts: msg.MaxAttempts,
		LastError:   msg.LastError,
		ScheduledAt: msg.ScheduledAt,
		CreatedAt:   msg.CreatedAt,
	}

	if msg.Payload != nil {
		copied.Payload = make([]byte, len(msg.Payload))
		copy(copied.Payload, msg.Payload)
	}

	if msg.Headers != nil {
		copied.Headers = make(map[string]string, len(msg.Headers))
		for k, v := range msg.Headers {
			copied.Headers[k] = v
		}
	}

	if msg.LastAttemptAt != nil {
		t := *msg.LastAttemptAt
		copied.LastAttemptAt = &t
	}

	if msg.ProcessedAt != nil {
		t := *msg.ProcessedAt
		copied.ProcessedAt = &t
	}

	return copied
}

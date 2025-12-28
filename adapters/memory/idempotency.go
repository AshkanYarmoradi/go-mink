package memory

import (
	"context"
	"sync"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Ensure interface compliance at compile time
var _ adapters.IdempotencyStore = (*IdempotencyStore)(nil)

// IdempotencyStore provides an in-memory implementation of adapters.IdempotencyStore.
// It is useful for testing and development but should not be used in production
// as it does not persist data across restarts.
type IdempotencyStore struct {
	mu      sync.RWMutex
	records map[string]*adapters.IdempotencyRecord

	// cleanupInterval is how often to run automatic cleanup
	cleanupInterval time.Duration
	// maxAge is the maximum age of records to keep
	maxAge time.Duration
	// stopCleanup signals the cleanup goroutine to stop
	stopCleanup chan struct{}
	// closeOnce ensures Close() is only executed once
	closeOnce sync.Once
	// cleanupStarted indicates whether cleanup goroutine has started
	cleanupStarted chan struct{}
}

// IdempotencyStoreOption configures an IdempotencyStore
type IdempotencyStoreOption func(*IdempotencyStore)

// WithCleanupInterval sets the interval for automatic cleanup.
// Set to 0 to disable automatic cleanup.
func WithCleanupInterval(interval time.Duration) IdempotencyStoreOption {
	return func(s *IdempotencyStore) {
		s.cleanupInterval = interval
	}
}

// WithMaxAge sets the maximum age for records.
// Records older than this will be cleaned up.
func WithMaxAge(maxAge time.Duration) IdempotencyStoreOption {
	return func(s *IdempotencyStore) {
		s.maxAge = maxAge
	}
}

// NewIdempotencyStore creates a new in-memory IdempotencyStore.
func NewIdempotencyStore(opts ...IdempotencyStoreOption) *IdempotencyStore {
	s := &IdempotencyStore{
		records:         make(map[string]*adapters.IdempotencyRecord),
		cleanupInterval: 0, // Disabled by default
		maxAge:          24 * time.Hour,
		stopCleanup:     make(chan struct{}),
		cleanupStarted:  make(chan struct{}),
	}

	for _, opt := range opts {
		opt(s)
	}

	if s.cleanupInterval > 0 {
		go s.startCleanup()
		// Wait for the cleanup goroutine to signal it has started
		<-s.cleanupStarted
	} else {
		// If no cleanup, close the channel so Close() doesn't block
		close(s.cleanupStarted)
	}

	return s
}

// startCleanup runs periodic cleanup of expired records
func (s *IdempotencyStore) startCleanup() {
	ticker := time.NewTicker(s.cleanupInterval)
	defer ticker.Stop()

	// Signal that the cleanup goroutine has started
	close(s.cleanupStarted)

	for {
		select {
		case <-ticker.C:
			_, _ = s.Cleanup(context.Background(), s.maxAge)
		case <-s.stopCleanup:
			return
		}
	}
}

// Close stops the cleanup goroutine and releases resources.
// It is safe to call Close() multiple times.
func (s *IdempotencyStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopCleanup)
	})
	return nil
}

// Exists checks if a record with the given key exists and is not expired.
func (s *IdempotencyStore) Exists(ctx context.Context, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.records[key]
	if !ok {
		return false, nil
	}

	// Check if expired
	if record.IsExpired() {
		return false, nil
	}

	return true, nil
}

// Store saves a new idempotency record.
func (s *IdempotencyStore) Store(ctx context.Context, record *adapters.IdempotencyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Make a copy to avoid external modifications
	recordCopy := &adapters.IdempotencyRecord{
		Key:         record.Key,
		CommandType: record.CommandType,
		AggregateID: record.AggregateID,
		Version:     record.Version,
		Response:    record.Response,
		Success:     record.Success,
		Error:       record.Error,
		ProcessedAt: record.ProcessedAt,
		ExpiresAt:   record.ExpiresAt,
	}

	s.records[record.Key] = recordCopy
	return nil
}

// Get retrieves an idempotency record by key.
// Returns nil if the record doesn't exist or is expired.
func (s *IdempotencyStore) Get(ctx context.Context, key string) (*adapters.IdempotencyRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.records[key]
	if !ok {
		return nil, nil
	}

	// Check if expired
	if record.IsExpired() {
		return nil, nil
	}

	// Return a copy to avoid external modifications
	recordCopy := &adapters.IdempotencyRecord{
		Key:         record.Key,
		CommandType: record.CommandType,
		AggregateID: record.AggregateID,
		Version:     record.Version,
		Response:    record.Response,
		Success:     record.Success,
		Error:       record.Error,
		ProcessedAt: record.ProcessedAt,
		ExpiresAt:   record.ExpiresAt,
	}

	return recordCopy, nil
}

// Delete removes an idempotency record by key.
func (s *IdempotencyStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.records, key)
	return nil
}

// Cleanup removes records older than the specified duration.
// Returns the number of records deleted.
func (s *IdempotencyStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	var count int64

	for key, record := range s.records {
		if record.ProcessedAt.Before(cutoff) || record.IsExpired() {
			delete(s.records, key)
			count++
		}
	}

	return count, nil
}

// Len returns the number of records in the store.
// Useful for testing.
func (s *IdempotencyStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// Clear removes all records from the store.
// Useful for testing.
func (s *IdempotencyStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = make(map[string]*adapters.IdempotencyRecord)
}

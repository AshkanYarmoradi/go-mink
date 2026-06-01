package mink

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
)

func TestIdempotencyMiddleware_ConcurrentDuplicates(t *testing.T) {
	store := memory.NewIdempotencyStore()
	defer func() { _ = store.Close() }()

	mw := IdempotencyMiddleware(DefaultIdempotencyConfig(store))

	var handlerCalls int32
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		atomic.AddInt32(&handlerCalls, 1)
		time.Sleep(20 * time.Millisecond) // widen the race window
		return NewSuccessResult("agg-1", 1), nil
	}
	wrapped := mw(handler)

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = wrapped(context.Background(), idempotentTestCommand{Value: "v", IdempotencyID: "same-key"})
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&handlerCalls),
		"handler must run exactly once for concurrent duplicate commands")
}

func TestIdempotency_ReservationUsesShortLease(t *testing.T) {
	// Default reservation lease is short (so a crashed handler does not block a
	// key for the full result TTL).
	cfg := DefaultIdempotencyConfig(newMockIdempotencyStore())
	assert.Equal(t, 5*time.Minute, cfg.ReservationTTL)
	assert.Equal(t, 24*time.Hour, cfg.TTL)

	// A reservation record expires after the (short) reservation TTL, not the TTL.
	rec := newProcessingRecord("k", "Cmd", cfg.ReservationTTL)
	assert.True(t, isProcessingRecord(rec))
	assert.WithinDuration(t, time.Now().Add(cfg.ReservationTTL), rec.ExpiresAt, 2*time.Second)
}

func TestIdempotencyMiddleware_FailClosed(t *testing.T) {
	store := newMockIdempotencyStore()
	store.getErr = errors.New("store down")
	config := DefaultIdempotencyConfig(store)
	config.FailClosed = true
	mw := IdempotencyMiddleware(config)

	handlerCalled := false
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		handlerCalled = true
		return NewSuccessResult("agg-1", 1), nil
	}

	result, err := mw(handler)(context.Background(), idempotencyTestCommand{Value: "x"})
	assert.Error(t, err)
	assert.True(t, result.IsError())
	assert.False(t, handlerCalled, "handler must not run when the store is down and FailClosed is set")
}

// Test command for idempotency tests

type idempotencyTestCommand struct {
	CommandBase
	Value string
}

func (c idempotencyTestCommand) CommandType() string { return "IdempotencyTestCommand" }
func (c idempotencyTestCommand) Validate() error     { return nil }

type idempotentTestCommand struct {
	CommandBase
	Value         string
	IdempotencyID string
}

func (c idempotentTestCommand) CommandType() string    { return "IdempotentTestCommand" }
func (c idempotentTestCommand) Validate() error        { return nil }
func (c idempotentTestCommand) IdempotencyKey() string { return c.IdempotencyID }

// unmarshalableCommand is a command that cannot be JSON marshaled
type unmarshalableCommand struct {
	CommandBase
}

func (c unmarshalableCommand) CommandType() string { return "UnmarshalableCommand" }
func (c unmarshalableCommand) Validate() error     { return nil }

// MarshalJSON always fails for testing the fallback path
func (c unmarshalableCommand) MarshalJSON() ([]byte, error) {
	return nil, errors.New("cannot marshal")
}

// Mock idempotency store for testing
type mockIdempotencyStore struct {
	records   map[string]*IdempotencyRecord
	existsErr error
	storeErr  error
	getErr    error
}

func newMockIdempotencyStore() *mockIdempotencyStore {
	return &mockIdempotencyStore{
		records: make(map[string]*IdempotencyRecord),
	}
}

func (s *mockIdempotencyStore) Exists(ctx context.Context, key string) (bool, error) {
	if s.existsErr != nil {
		return false, s.existsErr
	}
	_, ok := s.records[key]
	return ok, nil
}

func (s *mockIdempotencyStore) Store(ctx context.Context, record *IdempotencyRecord) error {
	if s.storeErr != nil {
		return s.storeErr
	}
	s.records[record.Key] = record
	return nil
}

func (s *mockIdempotencyStore) StoreIfAbsent(ctx context.Context, record *IdempotencyRecord) (bool, error) {
	if s.storeErr != nil {
		return false, s.storeErr
	}
	if _, ok := s.records[record.Key]; ok {
		return false, nil
	}
	s.records[record.Key] = record
	return true, nil
}

func (s *mockIdempotencyStore) Get(ctx context.Context, key string) (*IdempotencyRecord, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.records[key], nil
}

func (s *mockIdempotencyStore) Delete(ctx context.Context, key string) error {
	delete(s.records, key)
	return nil
}

func (s *mockIdempotencyStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	var count int64
	cutoff := time.Now().Add(-olderThan)
	for key, record := range s.records {
		if record.ProcessedAt.Before(cutoff) {
			delete(s.records, key)
			count++
		}
	}
	return count, nil
}

// storeFailingIdempotencyStore behaves like the base mock but always fails Store,
// so the reservation (StoreIfAbsent) succeeds while persisting the final record
// fails — the exact situation that should release the reservation.
type storeFailingIdempotencyStore struct {
	*mockIdempotencyStore
}

func (s *storeFailingIdempotencyStore) Store(ctx context.Context, record *IdempotencyRecord) error {
	return errors.New("store failed")
}

func TestIdempotencyMiddleware_StoreFailureReleasesReservation(t *testing.T) {
	base := newMockIdempotencyStore()
	store := &storeFailingIdempotencyStore{mockIdempotencyStore: base}
	config := DefaultIdempotencyConfig(store)
	mw := IdempotencyMiddleware(config)

	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewSuccessResult("agg-1", 1), nil
	}

	cmd := idempotencyTestCommand{Value: "test"}
	key := GetIdempotencyKey(cmd)

	result, err := mw(handler)(context.Background(), cmd)
	require.NoError(t, err)
	assert.True(t, result.IsSuccess())

	// Persisting the result failed; the processing reservation must have been
	// released so a later retry isn't rejected as "command in progress".
	_, exists := base.records[key]
	assert.False(t, exists, "stale reservation should be removed when Store fails")
}

func TestIdempotencyRecord(t *testing.T) {
	t.Run("NewIdempotencyRecord from success", func(t *testing.T) {
		result := NewSuccessResult("agg-1", 5)
		record := NewIdempotencyRecord("key-1", "TestCommand", result, time.Hour)

		assert.Equal(t, "key-1", record.Key)
		assert.Equal(t, "TestCommand", record.CommandType)
		assert.Equal(t, "agg-1", record.AggregateID)
		assert.Equal(t, int64(5), record.Version)
		assert.True(t, record.Success)
		assert.Empty(t, record.Error)
		assert.False(t, record.ProcessedAt.IsZero())
		assert.False(t, record.ExpiresAt.IsZero())
	})

	t.Run("NewIdempotencyRecord from error", func(t *testing.T) {
		result := NewErrorResult(errors.New("test error"))
		record := NewIdempotencyRecord("key-1", "TestCommand", result, time.Hour)

		assert.False(t, record.Success)
		assert.Equal(t, "test error", record.Error)
	})

	t.Run("IsExpired", func(t *testing.T) {
		record := &IdempotencyRecord{
			ExpiresAt: time.Now().Add(time.Hour),
		}
		assert.False(t, record.IsExpired())

		record.ExpiresAt = time.Now().Add(-time.Hour)
		assert.True(t, record.IsExpired())
	})

	t.Run("ToResult success", func(t *testing.T) {
		record := &IdempotencyRecord{
			AggregateID: "agg-1",
			Version:     5,
			Success:     true,
		}
		result := IdempotencyRecordToResult(record)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, "agg-1", result.AggregateID)
		assert.Equal(t, int64(5), result.Version)
	})

	t.Run("ToResult error", func(t *testing.T) {
		record := &IdempotencyRecord{
			Key:     "key-1",
			Success: false,
			Error:   "original error",
		}
		result := IdempotencyRecordToResult(record)
		assert.True(t, result.IsError())
		assert.ErrorIs(t, result.Error, ErrCommandAlreadyProcessed)
	})

	t.Run("ToResult unknown error", func(t *testing.T) {
		record := &IdempotencyRecord{
			Key:     "key-1",
			Success: false,
		}
		result := IdempotencyRecordToResult(record)
		assert.True(t, result.IsError())
	})
}

func TestIdempotencyReplayError(t *testing.T) {
	t.Run("Error message with message", func(t *testing.T) {
		err := &IdempotencyReplayError{Key: "key-1", Message: "test error"}
		assert.Contains(t, err.Error(), "key-1")
		assert.Contains(t, err.Error(), "test error")
		assert.Contains(t, err.Error(), "already processed")
	})

	t.Run("Error message without message", func(t *testing.T) {
		err := &IdempotencyReplayError{Key: "key-1"}
		assert.Contains(t, err.Error(), "key-1")
		assert.Contains(t, err.Error(), "already processed")
	})

	t.Run("Is ErrCommandAlreadyProcessed", func(t *testing.T) {
		err := &IdempotencyReplayError{Key: "key-1"}
		assert.ErrorIs(t, err, ErrCommandAlreadyProcessed)
	})

	t.Run("Unwrap", func(t *testing.T) {
		err := &IdempotencyReplayError{Key: "key-1"}
		assert.Equal(t, ErrCommandAlreadyProcessed, err.Unwrap())
	})
}

func TestGenerateIdempotencyKey(t *testing.T) {
	t.Run("generates consistent key", func(t *testing.T) {
		cmd := idempotencyTestCommand{Value: "test"}
		key1 := GenerateIdempotencyKey(cmd)
		key2 := GenerateIdempotencyKey(cmd)
		assert.Equal(t, key1, key2)
	})

	t.Run("different values produce different keys", func(t *testing.T) {
		cmd1 := idempotencyTestCommand{Value: "test1"}
		cmd2 := idempotencyTestCommand{Value: "test2"}
		key1 := GenerateIdempotencyKey(cmd1)
		key2 := GenerateIdempotencyKey(cmd2)
		assert.NotEqual(t, key1, key2)
	})

	t.Run("key includes command type", func(t *testing.T) {
		cmd := idempotencyTestCommand{Value: "test"}
		key := GenerateIdempotencyKey(cmd)
		assert.Contains(t, key, "IdempotencyTestCommand:")
	})

	t.Run("handles unmarshallable command with fallback", func(t *testing.T) {
		cmd := unmarshalableCommand{}
		key := GenerateIdempotencyKey(cmd)
		// Should still generate a key (fallback path)
		assert.Contains(t, key, "UnmarshalableCommand:")
	})

	t.Run("fallback generates deterministic key", func(t *testing.T) {
		// Verify that unmarshallable commands produce identical keys on repeated calls
		cmd := unmarshalableCommand{}
		key1 := GenerateIdempotencyKey(cmd)
		key2 := GenerateIdempotencyKey(cmd)
		assert.Equal(t, key1, key2, "fallback should generate deterministic keys")
		assert.Contains(t, key1, ":type-only:", "fallback key should include type-only marker")
	})
}

func TestGetIdempotencyKey(t *testing.T) {
	t.Run("uses IdempotentCommand interface", func(t *testing.T) {
		cmd := idempotentTestCommand{IdempotencyID: "custom-key"}
		key := GetIdempotencyKey(cmd)
		assert.Equal(t, "custom-key", key)
	})

	t.Run("falls back to GenerateIdempotencyKey", func(t *testing.T) {
		cmd := idempotencyTestCommand{Value: "test"}
		key := GetIdempotencyKey(cmd)
		assert.Contains(t, key, "IdempotencyTestCommand:")
	})
}

func TestIdempotencyMiddleware(t *testing.T) {
	t.Run("processes new command", func(t *testing.T) {
		store := newMockIdempotencyStore()
		config := DefaultIdempotencyConfig(store)
		mw := IdempotencyMiddleware(config)

		handlerCalled := false
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			handlerCalled = true
			return NewSuccessResult("agg-1", 1), nil
		}

		cmd := idempotencyTestCommand{Value: "test"}
		result, err := mw(handler)(context.Background(), cmd)

		assert.True(t, handlerCalled)
		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Len(t, store.records, 1)
	})

	t.Run("replays existing command", func(t *testing.T) {
		store := newMockIdempotencyStore()
		config := DefaultIdempotencyConfig(store)
		mw := IdempotencyMiddleware(config)

		cmd := idempotencyTestCommand{Value: "test"}
		key := GetIdempotencyKey(cmd)

		// Pre-store record
		store.records[key] = &IdempotencyRecord{
			Key:         key,
			AggregateID: "existing-agg",
			Version:     10,
			Success:     true,
			ExpiresAt:   time.Now().Add(time.Hour),
		}

		handlerCalled := false
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			handlerCalled = true
			return NewSuccessResult("new-agg", 1), nil
		}

		result, err := mw(handler)(context.Background(), cmd)

		assert.False(t, handlerCalled)
		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
		assert.Equal(t, "existing-agg", result.AggregateID)
		assert.Equal(t, int64(10), result.Version)
	})

	t.Run("processes expired record", func(t *testing.T) {
		store := newMockIdempotencyStore()
		config := DefaultIdempotencyConfig(store)
		mw := IdempotencyMiddleware(config)

		cmd := idempotencyTestCommand{Value: "test"}
		key := GetIdempotencyKey(cmd)

		// Pre-store expired record
		store.records[key] = &IdempotencyRecord{
			Key:       key,
			Success:   true,
			ExpiresAt: time.Now().Add(-time.Hour), // Expired
		}

		handlerCalled := false
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			handlerCalled = true
			return NewSuccessResult("new-agg", 1), nil
		}

		result, err := mw(handler)(context.Background(), cmd)

		assert.True(t, handlerCalled)
		require.NoError(t, err)
		assert.Equal(t, "new-agg", result.AggregateID)
	})

	t.Run("skips specified command types", func(t *testing.T) {
		store := newMockIdempotencyStore()
		config := DefaultIdempotencyConfig(store)
		config.SkipCommands = []string{"IdempotencyTestCommand"}
		mw := IdempotencyMiddleware(config)

		handlerCalled := false
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			handlerCalled = true
			return NewSuccessResult("agg-1", 1), nil
		}

		cmd := idempotencyTestCommand{Value: "test"}
		_, _ = mw(handler)(context.Background(), cmd)

		assert.True(t, handlerCalled)
		assert.Len(t, store.records, 0) // Not stored
	})

	t.Run("continues on store error", func(t *testing.T) {
		store := newMockIdempotencyStore()
		store.getErr = errors.New("store error")
		config := DefaultIdempotencyConfig(store)
		mw := IdempotencyMiddleware(config)

		handlerCalled := false
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			handlerCalled = true
			return NewSuccessResult("agg-1", 1), nil
		}

		cmd := idempotencyTestCommand{Value: "test"}
		result, err := mw(handler)(context.Background(), cmd)

		assert.True(t, handlerCalled)
		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
	})

	t.Run("stores errors when configured", func(t *testing.T) {
		store := newMockIdempotencyStore()
		config := DefaultIdempotencyConfig(store)
		config.StoreErrors = true
		mw := IdempotencyMiddleware(config)

		handlerErr := errors.New("handler error")
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewErrorResult(handlerErr), handlerErr
		}

		cmd := idempotencyTestCommand{Value: "test"}
		_, _ = mw(handler)(context.Background(), cmd)

		assert.Len(t, store.records, 1)
		key := GetIdempotencyKey(cmd)
		assert.False(t, store.records[key].Success)
	})

	t.Run("does not store errors by default", func(t *testing.T) {
		store := newMockIdempotencyStore()
		config := DefaultIdempotencyConfig(store)
		config.StoreErrors = false
		mw := IdempotencyMiddleware(config)

		handlerErr := errors.New("handler error")
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewErrorResult(handlerErr), handlerErr
		}

		cmd := idempotencyTestCommand{Value: "test"}
		_, _ = mw(handler)(context.Background(), cmd)

		assert.Len(t, store.records, 0)
	})

	t.Run("does not store error result when StoreErrors true but no cmdErr", func(t *testing.T) {
		store := newMockIdempotencyStore()
		config := DefaultIdempotencyConfig(store)
		config.StoreErrors = true
		mw := IdempotencyMiddleware(config)

		// Return error result but nil error (no cmdErr)
		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewErrorResult(errors.New("result error")), nil // cmdErr is nil
		}

		cmd := idempotencyTestCommand{Value: "test"}
		_, _ = mw(handler)(context.Background(), cmd)

		// Should NOT store because !result.IsSuccess() && cmdErr == nil
		assert.Len(t, store.records, 0)
	})

	t.Run("uses custom key generator", func(t *testing.T) {
		store := newMockIdempotencyStore()
		config := DefaultIdempotencyConfig(store)
		config.KeyGenerator = func(cmd Command) string {
			return "custom-key"
		}
		mw := IdempotencyMiddleware(config)

		handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
			return NewSuccessResult("agg-1", 1), nil
		}

		cmd := idempotencyTestCommand{Value: "test"}
		_, _ = mw(handler)(context.Background(), cmd)

		assert.NotNil(t, store.records["custom-key"])
	})

	t.Run("default TTL is 24 hours", func(t *testing.T) {
		config := DefaultIdempotencyConfig(nil)
		assert.Equal(t, 24*time.Hour, config.TTL)
	})
}

func TestIdempotencyKeyPrefix(t *testing.T) {
	t.Run("adds prefix to key", func(t *testing.T) {
		generator := IdempotencyKeyPrefix("myprefix")
		cmd := idempotencyTestCommand{Value: "test"}
		key := generator(cmd)
		assert.Contains(t, key, "myprefix:")
	})
}

func TestIdempotencyKeyFromField(t *testing.T) {
	type cmdWithField struct {
		idempotencyTestCommand
		RequestID string
	}

	t.Run("extracts key from field", func(t *testing.T) {
		generator := IdempotencyKeyFromField(func(cmd Command) string {
			if c, ok := cmd.(cmdWithField); ok {
				return c.RequestID
			}
			return ""
		})

		cmd := cmdWithField{RequestID: "req-123"}
		key := generator(cmd)
		assert.Contains(t, key, "req-123")
	})

	t.Run("falls back when field empty", func(t *testing.T) {
		generator := IdempotencyKeyFromField(func(cmd Command) string {
			return ""
		})

		cmd := idempotencyTestCommand{Value: "test"}
		key := generator(cmd)
		assert.Contains(t, key, "IdempotencyTestCommand:")
	})
}

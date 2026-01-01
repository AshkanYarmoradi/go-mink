package adapters

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionConstants(t *testing.T) {
	t.Run("version constants have expected values", func(t *testing.T) {
		assert.Equal(t, int64(-1), AnyVersion)
		assert.Equal(t, int64(0), NoStream)
		assert.Equal(t, int64(-2), StreamExists)
	})
}

func TestExtractCategory(t *testing.T) {
	tests := []struct {
		name     string
		streamID string
		expected string
	}{
		{
			name:     "standard format Order-123",
			streamID: "Order-123",
			expected: "Order",
		},
		{
			name:     "standard format User-abc",
			streamID: "User-abc",
			expected: "User",
		},
		{
			name:     "multiple hyphens takes first part",
			streamID: "User-abc-def-ghi",
			expected: "User",
		},
		{
			name:     "no hyphen returns entire ID",
			streamID: "SingleWord",
			expected: "SingleWord",
		},
		{
			name:     "empty string returns empty",
			streamID: "",
			expected: "",
		},
		{
			name:     "starts with hyphen returns empty",
			streamID: "-StartsWithHyphen",
			expected: "",
		},
		{
			name:     "ends with hyphen",
			streamID: "EndsWithHyphen-",
			expected: "EndsWithHyphen",
		},
		{
			name:     "only hyphen",
			streamID: "-",
			expected: "",
		},
		{
			name:     "numeric category",
			streamID: "123-456",
			expected: "123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractCategory(tt.streamID)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConcurrencyError(t *testing.T) {
	t.Run("NewConcurrencyError creates error with correct fields", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", 5, 3)

		assert.Equal(t, "Order-123", err.StreamID)
		assert.Equal(t, int64(5), err.ExpectedVersion)
		assert.Equal(t, int64(3), err.ActualVersion)
	})

	t.Run("Error method returns formatted message", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", 5, 3)

		expected := `mink: concurrency conflict on stream "Order-123": expected version 5, got 3`
		assert.Equal(t, expected, err.Error())
	})

	t.Run("Is returns true for ErrConcurrencyConflict", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", 5, 3)

		assert.True(t, errors.Is(err, ErrConcurrencyConflict))
	})

	t.Run("Is returns false for other errors", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", 5, 3)

		assert.False(t, errors.Is(err, ErrStreamNotFound))
		assert.False(t, errors.Is(err, ErrEmptyStreamID))
		assert.False(t, errors.Is(err, ErrNoEvents))
	})

	t.Run("works with NoStream expected version", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", NoStream, 1)

		assert.Contains(t, err.Error(), "expected version 0")
	})

	t.Run("works with StreamExists expected version", func(t *testing.T) {
		err := NewConcurrencyError("Order-123", StreamExists, 0)

		assert.Contains(t, err.Error(), "expected version -2")
	})
}

func TestStreamNotFoundError(t *testing.T) {
	t.Run("NewStreamNotFoundError creates error with correct field", func(t *testing.T) {
		err := NewStreamNotFoundError("Order-123")

		assert.Equal(t, "Order-123", err.StreamID)
	})

	t.Run("Error method returns formatted message", func(t *testing.T) {
		err := NewStreamNotFoundError("Order-123")

		expected := `mink: stream "Order-123" not found`
		assert.Equal(t, expected, err.Error())
	})

	t.Run("Is returns true for ErrStreamNotFound", func(t *testing.T) {
		err := NewStreamNotFoundError("Order-123")

		assert.True(t, errors.Is(err, ErrStreamNotFound))
	})

	t.Run("Is returns false for other errors", func(t *testing.T) {
		err := NewStreamNotFoundError("Order-123")

		assert.False(t, errors.Is(err, ErrConcurrencyConflict))
		assert.False(t, errors.Is(err, ErrEmptyStreamID))
		assert.False(t, errors.Is(err, ErrNoEvents))
	})

	t.Run("handles empty stream ID", func(t *testing.T) {
		err := NewStreamNotFoundError("")

		assert.Contains(t, err.Error(), `stream ""`)
	})
}

func TestCheckVersion(t *testing.T) {
	t.Run("AnyVersion always succeeds", func(t *testing.T) {
		tests := []struct {
			name    string
			current int64
			exists  bool
		}{
			{"stream exists with version 0", 0, true},
			{"stream exists with version 5", 5, true},
			{"stream does not exist", 0, false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := CheckVersion("Order-123", AnyVersion, tt.current, tt.exists)
				assert.NoError(t, err)
			})
		}
	})

	t.Run("NoStream succeeds when stream does not exist", func(t *testing.T) {
		err := CheckVersion("Order-123", NoStream, 0, false)
		assert.NoError(t, err)
	})

	t.Run("NoStream fails when stream exists", func(t *testing.T) {
		err := CheckVersion("Order-123", NoStream, 5, true)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrConcurrencyConflict))

		var concErr *ConcurrencyError
		require.True(t, errors.As(err, &concErr))
		assert.Equal(t, "Order-123", concErr.StreamID)
		assert.Equal(t, NoStream, concErr.ExpectedVersion)
		assert.Equal(t, int64(5), concErr.ActualVersion)
	})

	t.Run("StreamExists succeeds when stream exists", func(t *testing.T) {
		err := CheckVersion("Order-123", StreamExists, 5, true)
		assert.NoError(t, err)
	})

	t.Run("StreamExists fails when stream does not exist", func(t *testing.T) {
		err := CheckVersion("Order-123", StreamExists, 0, false)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrStreamNotFound))

		var notFoundErr *StreamNotFoundError
		require.True(t, errors.As(err, &notFoundErr))
		assert.Equal(t, "Order-123", notFoundErr.StreamID)
	})

	t.Run("positive version succeeds when current matches expected", func(t *testing.T) {
		err := CheckVersion("Order-123", 5, 5, true)
		assert.NoError(t, err)
	})

	t.Run("positive version fails when current differs from expected", func(t *testing.T) {
		err := CheckVersion("Order-123", 5, 3, true)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrConcurrencyConflict))

		var concErr *ConcurrencyError
		require.True(t, errors.As(err, &concErr))
		assert.Equal(t, int64(5), concErr.ExpectedVersion)
		assert.Equal(t, int64(3), concErr.ActualVersion)
	})

	t.Run("negative version other than AnyVersion/NoStream/StreamExists returns ErrInvalidVersion", func(t *testing.T) {
		invalidVersions := []int64{-3, -4, -10, -100}

		for _, v := range invalidVersions {
			err := CheckVersion("Order-123", v, 5, true)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidVersion))
		}
	})

	t.Run("version 1 succeeds when stream has version 1", func(t *testing.T) {
		err := CheckVersion("Order-123", 1, 1, true)
		assert.NoError(t, err)
	})

	t.Run("version 1 fails when stream has version 0", func(t *testing.T) {
		err := CheckVersion("Order-123", 1, 0, true)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrConcurrencyConflict))
	})
}

func TestCopyIdempotencyRecord(t *testing.T) {
	t.Run("returns nil for nil input", func(t *testing.T) {
		result := CopyIdempotencyRecord(nil)
		assert.Nil(t, result)
	})

	t.Run("creates deep copy of record", func(t *testing.T) {
		original := &IdempotencyRecord{
			Key:         "test-key",
			CommandType: "CreateOrder",
			AggregateID: "Order-123",
			Version:     5,
			Response:    []byte(`{"result":"success"}`),
			Success:     true,
			Error:       "",
			ProcessedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}

		copied := CopyIdempotencyRecord(original)

		// Verify all fields are copied
		assert.Equal(t, original.Key, copied.Key)
		assert.Equal(t, original.CommandType, copied.CommandType)
		assert.Equal(t, original.AggregateID, copied.AggregateID)
		assert.Equal(t, original.Version, copied.Version)
		assert.Equal(t, original.Response, copied.Response)
		assert.Equal(t, original.Success, copied.Success)
		assert.Equal(t, original.Error, copied.Error)
		assert.Equal(t, original.ProcessedAt, copied.ProcessedAt)
		assert.Equal(t, original.ExpiresAt, copied.ExpiresAt)

		// Verify it's a different pointer
		assert.NotSame(t, original, copied)
	})

	t.Run("modifications to copy do not affect original", func(t *testing.T) {
		original := &IdempotencyRecord{
			Key:     "test-key",
			Version: 5,
		}

		copied := CopyIdempotencyRecord(original)
		copied.Key = "modified-key"
		copied.Version = 10

		assert.Equal(t, "test-key", original.Key)
		assert.Equal(t, int64(5), original.Version)
	})

	t.Run("copies record with error", func(t *testing.T) {
		original := &IdempotencyRecord{
			Key:     "test-key",
			Success: false,
			Error:   "something went wrong",
		}

		copied := CopyIdempotencyRecord(original)

		assert.False(t, copied.Success)
		assert.Equal(t, "something went wrong", copied.Error)
	})

	t.Run("copies record with empty fields", func(t *testing.T) {
		original := &IdempotencyRecord{
			Key: "minimal-key",
		}

		copied := CopyIdempotencyRecord(original)

		assert.Equal(t, "minimal-key", copied.Key)
		assert.Empty(t, copied.CommandType)
		assert.Empty(t, copied.AggregateID)
		assert.Equal(t, int64(0), copied.Version)
		assert.Nil(t, copied.Response)
	})
}

func TestDefaultLimit(t *testing.T) {
	tests := []struct {
		name         string
		limit        int
		defaultValue int
		expected     int
	}{
		{
			name:         "zero limit returns default",
			limit:        0,
			defaultValue: 100,
			expected:     100,
		},
		{
			name:         "negative limit returns default",
			limit:        -1,
			defaultValue: 100,
			expected:     100,
		},
		{
			name:         "very negative limit returns default",
			limit:        -1000,
			defaultValue: 50,
			expected:     50,
		},
		{
			name:         "positive limit is used",
			limit:        50,
			defaultValue: 100,
			expected:     50,
		},
		{
			name:         "limit of 1 is used",
			limit:        1,
			defaultValue: 100,
			expected:     1,
		},
		{
			name:         "large limit is used",
			limit:        10000,
			defaultValue: 100,
			expected:     10000,
		},
		{
			name:         "zero default with zero limit returns zero",
			limit:        0,
			defaultValue: 0,
			expected:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DefaultLimit(tt.limit, tt.defaultValue)
			assert.Equal(t, tt.expected, result)
		})
	}
}

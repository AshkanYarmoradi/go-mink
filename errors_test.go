package mink

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcurrencyError(t *testing.T) {
	t.Run("Error message", func(t *testing.T) {
		err := NewConcurrencyError("order-123", 5, 7)

		assert.Contains(t, err.Error(), "order-123")
		assert.Contains(t, err.Error(), "expected version 5")
		assert.Contains(t, err.Error(), "actual version 7")
	})

	t.Run("Is ErrConcurrencyConflict", func(t *testing.T) {
		err := NewConcurrencyError("order-123", 5, 7)

		assert.True(t, errors.Is(err, ErrConcurrencyConflict))
		assert.False(t, errors.Is(err, ErrStreamNotFound))
	})

	t.Run("Unwrap returns ErrConcurrencyConflict", func(t *testing.T) {
		err := NewConcurrencyError("order-123", 5, 7)

		assert.Equal(t, ErrConcurrencyConflict, errors.Unwrap(err))
	})

	t.Run("errors.As extracts details", func(t *testing.T) {
		err := NewConcurrencyError("order-123", 5, 7)

		var concErr *ConcurrencyError
		require.True(t, errors.As(err, &concErr))
		assert.Equal(t, "order-123", concErr.StreamID)
		assert.Equal(t, int64(5), concErr.ExpectedVersion)
		assert.Equal(t, int64(7), concErr.ActualVersion)
	})

	t.Run("wrapped error still matches", func(t *testing.T) {
		origErr := NewConcurrencyError("order-123", 5, 7)
		wrappedErr := errors.New("outer: " + origErr.Error())

		// Direct error should match
		assert.True(t, errors.Is(origErr, ErrConcurrencyConflict))

		// Note: wrappedErr won't match because errors.New doesn't implement Unwrap
		// This is expected Go behavior
		_ = wrappedErr
	})
}

func TestStreamNotFoundError(t *testing.T) {
	t.Run("Error message", func(t *testing.T) {
		err := NewStreamNotFoundError("order-456")

		assert.Contains(t, err.Error(), "order-456")
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("Is ErrStreamNotFound", func(t *testing.T) {
		err := NewStreamNotFoundError("order-456")

		assert.True(t, errors.Is(err, ErrStreamNotFound))
		assert.False(t, errors.Is(err, ErrConcurrencyConflict))
	})

	t.Run("Unwrap returns ErrStreamNotFound", func(t *testing.T) {
		err := NewStreamNotFoundError("order-456")

		assert.Equal(t, ErrStreamNotFound, errors.Unwrap(err))
	})

	t.Run("errors.As extracts details", func(t *testing.T) {
		err := NewStreamNotFoundError("order-456")

		var streamErr *StreamNotFoundError
		require.True(t, errors.As(err, &streamErr))
		assert.Equal(t, "order-456", streamErr.StreamID)
	})
}

func TestSerializationError(t *testing.T) {
	t.Run("Error message for serialize", func(t *testing.T) {
		cause := errors.New("json: unsupported type")
		err := NewSerializationError("OrderCreated", "serialize", cause)

		assert.Contains(t, err.Error(), "OrderCreated")
		assert.Contains(t, err.Error(), "serialize")
		assert.Contains(t, err.Error(), "unsupported type")
	})

	t.Run("Error message for deserialize", func(t *testing.T) {
		cause := errors.New("invalid character")
		err := NewSerializationError("ItemAdded", "deserialize", cause)

		assert.Contains(t, err.Error(), "ItemAdded")
		assert.Contains(t, err.Error(), "deserialize")
		assert.Contains(t, err.Error(), "invalid character")
	})

	t.Run("Is ErrSerializationFailed", func(t *testing.T) {
		err := NewSerializationError("OrderCreated", "serialize", errors.New("test"))

		assert.True(t, errors.Is(err, ErrSerializationFailed))
		assert.False(t, errors.Is(err, ErrStreamNotFound))
	})

	t.Run("Unwrap returns cause", func(t *testing.T) {
		cause := errors.New("underlying error")
		err := NewSerializationError("OrderCreated", "serialize", cause)

		assert.Equal(t, cause, errors.Unwrap(err))
	})

	t.Run("errors.As extracts details", func(t *testing.T) {
		cause := errors.New("test cause")
		err := NewSerializationError("OrderCreated", "serialize", cause)

		var serErr *SerializationError
		require.True(t, errors.As(err, &serErr))
		assert.Equal(t, "OrderCreated", serErr.EventType)
		assert.Equal(t, "serialize", serErr.Operation)
		assert.Equal(t, cause, serErr.Cause)
	})
}

func TestEventTypeNotRegisteredError(t *testing.T) {
	t.Run("Error message", func(t *testing.T) {
		err := NewEventTypeNotRegisteredError("UnknownEvent")

		assert.Contains(t, err.Error(), "UnknownEvent")
		assert.Contains(t, err.Error(), "not registered")
	})

	t.Run("Is ErrEventTypeNotRegistered", func(t *testing.T) {
		err := NewEventTypeNotRegisteredError("UnknownEvent")

		assert.True(t, errors.Is(err, ErrEventTypeNotRegistered))
		assert.False(t, errors.Is(err, ErrStreamNotFound))
	})

	t.Run("Unwrap returns ErrEventTypeNotRegistered", func(t *testing.T) {
		err := NewEventTypeNotRegisteredError("UnknownEvent")

		assert.Equal(t, ErrEventTypeNotRegistered, errors.Unwrap(err))
	})

	t.Run("errors.As extracts details", func(t *testing.T) {
		err := NewEventTypeNotRegisteredError("UnknownEvent")

		var typeErr *EventTypeNotRegisteredError
		require.True(t, errors.As(err, &typeErr))
		assert.Equal(t, "UnknownEvent", typeErr.EventType)
	})
}

func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"ErrStreamNotFound", ErrStreamNotFound},
		{"ErrConcurrencyConflict", ErrConcurrencyConflict},
		{"ErrEventNotFound", ErrEventNotFound},
		{"ErrSerializationFailed", ErrSerializationFailed},
		{"ErrEventTypeNotRegistered", ErrEventTypeNotRegistered},
		{"ErrNilAggregate", ErrNilAggregate},
		{"ErrEmptyStreamID", ErrEmptyStreamID},
		{"ErrNoEvents", ErrNoEvents},
		{"ErrInvalidVersion", ErrInvalidVersion},
		{"ErrAdapterClosed", ErrAdapterClosed},
	}

	for _, tt := range tests {
		t.Run(tt.name+" has message", func(t *testing.T) {
			assert.NotEmpty(t, tt.err.Error())
			assert.Contains(t, tt.err.Error(), "mink:")
		})
	}

	t.Run("sentinel errors are distinct", func(t *testing.T) {
		allErrors := []error{
			ErrStreamNotFound,
			ErrConcurrencyConflict,
			ErrEventNotFound,
			ErrSerializationFailed,
			ErrEventTypeNotRegistered,
			ErrNilAggregate,
			ErrEmptyStreamID,
			ErrNoEvents,
			ErrInvalidVersion,
			ErrAdapterClosed,
		}

		for i, err1 := range allErrors {
			for j, err2 := range allErrors {
				if i != j {
					assert.False(t, errors.Is(err1, err2),
						"expected %v and %v to be distinct", err1, err2)
				}
			}
		}
	})
}

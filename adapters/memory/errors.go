package memory

import (
	"strconv"

	"go-mink.dev/adapters"
)

// Sentinel errors for the memory adapter.
// These are aliases to the adapters package errors for compatibility with errors.Is().
var (
	// ErrAdapterClosed is returned when an operation is attempted on a closed adapter.
	ErrAdapterClosed = adapters.ErrAdapterClosed

	// ErrEmptyStreamID is returned when an empty stream ID is provided.
	ErrEmptyStreamID = adapters.ErrEmptyStreamID

	// ErrNoEvents is returned when attempting to append zero events.
	ErrNoEvents = adapters.ErrNoEvents

	// ErrConcurrencyConflict is returned when optimistic concurrency check fails.
	ErrConcurrencyConflict = adapters.ErrConcurrencyConflict

	// ErrStreamNotFound is returned when a stream does not exist.
	ErrStreamNotFound = adapters.ErrStreamNotFound

	// ErrInvalidVersion is returned when an invalid version is specified.
	ErrInvalidVersion = adapters.ErrInvalidVersion
)

// ConcurrencyError is an alias for adapters.ConcurrencyError for backward compatibility.
type ConcurrencyError = adapters.ConcurrencyError

// StreamNotFoundError is an alias for adapters.StreamNotFoundError for backward compatibility.
type StreamNotFoundError = adapters.StreamNotFoundError

// NewConcurrencyError is an alias for adapters.NewConcurrencyError for backward compatibility.
var NewConcurrencyError = adapters.NewConcurrencyError

// NewStreamNotFoundError is an alias for adapters.NewStreamNotFoundError for backward compatibility.
var NewStreamNotFoundError = adapters.NewStreamNotFoundError

// SubscriptionDropError reports that a live event could not be delivered to a
// subscriber because its channel buffer was full and the event was dropped.
// It is passed to SubscriptionOptions.OnError (when provided) so callers can
// detect lossy live delivery from the memory adapter. The identifying fields
// pinpoint the dropped event so the caller may, for example, trigger a
// checkpoint-based catch-up from GlobalPosition.
type SubscriptionDropError struct {
	// StreamID is the stream the dropped event belonged to.
	StreamID string

	// GlobalPosition is the global position of the dropped event.
	GlobalPosition uint64
}

// Error returns the error message.
func (e *SubscriptionDropError) Error() string {
	return "mink/memory: dropped live event for stream " + e.StreamID +
		" at global position " + strconv.FormatUint(e.GlobalPosition, 10) +
		" (subscriber buffer full)"
}

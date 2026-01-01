package memory

import (
	"github.com/AshkanYarmoradi/go-mink/adapters"
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

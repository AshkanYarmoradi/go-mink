package mink

import (
	"errors"
	"fmt"
)

// Erasure-related sentinel errors.
var (
	// ErrErasureFailed indicates a data erasure operation failed.
	ErrErasureFailed = errors.New("mink: erasure failed")

	// ErrErasureSubjectRequired indicates the subject ID was not provided.
	ErrErasureSubjectRequired = errors.New("mink: subject ID is required for erasure")

	// ErrNoErasureSources indicates none of streams, filter, or key IDs was provided.
	ErrNoErasureSources = errors.New("mink: streams, filter, or key IDs are required for erasure")

	// ErrErasureNotConfigured indicates the store has no field encryption, so there
	// is nothing to crypto-shred.
	ErrErasureNotConfigured = errors.New("mink: erasure requires field encryption (WithFieldEncryption)")

	// ErrErasureScanNotSupported indicates the adapter does not support event
	// scanning. Provide explicit stream IDs or key IDs instead.
	ErrErasureScanNotSupported = errors.New("mink: adapter does not support event scanning; provide explicit stream IDs or key IDs")
)

// ErasureError provides detailed information about a data erasure failure.
type ErasureError struct {
	SubjectID string
	Cause     error
}

// Error returns the error message.
func (e *ErasureError) Error() string {
	if e.SubjectID != "" {
		return fmt.Sprintf("mink: erasure failed for subject %q: %v", e.SubjectID, e.Cause)
	}
	return fmt.Sprintf("mink: erasure failed: %v", e.Cause)
}

// Is reports whether this error matches the target error.
func (e *ErasureError) Is(target error) bool {
	return target == ErrErasureFailed
}

// Unwrap returns the underlying cause for errors.Unwrap().
func (e *ErasureError) Unwrap() error {
	return e.Cause
}

// NewErasureError creates a new ErasureError.
func NewErasureError(subjectID string, cause error) *ErasureError {
	return &ErasureError{
		SubjectID: subjectID,
		Cause:     cause,
	}
}

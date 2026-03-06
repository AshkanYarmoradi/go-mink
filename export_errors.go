package mink

import (
	"errors"
	"fmt"
)

// Export-related sentinel errors.
var (
	// ErrExportFailed indicates a data export operation failed.
	ErrExportFailed = errors.New("mink: export failed")

	// ErrSubjectIDRequired indicates the subject ID was not provided in the export request.
	ErrSubjectIDRequired = errors.New("mink: subject ID is required for export")

	// ErrNoExportSources indicates neither streams nor a filter was provided.
	ErrNoExportSources = errors.New("mink: either streams or filter is required for export")

	// ErrExportScanNotSupported indicates the adapter does not support event scanning.
	// Provide explicit stream IDs in the ExportRequest instead.
	ErrExportScanNotSupported = errors.New("mink: adapter does not support event scanning; provide explicit stream IDs")
)

// ExportError provides detailed information about a data export failure.
type ExportError struct {
	SubjectID string
	Cause     error
}

// Error returns the error message.
func (e *ExportError) Error() string {
	if e.SubjectID != "" {
		return fmt.Sprintf("mink: export failed for subject %q: %v", e.SubjectID, e.Cause)
	}
	return fmt.Sprintf("mink: export failed: %v", e.Cause)
}

// Is reports whether this error matches the target error.
func (e *ExportError) Is(target error) bool {
	return target == ErrExportFailed
}

// Unwrap returns the underlying cause for errors.Unwrap().
func (e *ExportError) Unwrap() error {
	return e.Cause
}

// NewExportError creates a new ExportError.
func NewExportError(subjectID string, cause error) *ExportError {
	return &ExportError{
		SubjectID: subjectID,
		Cause:     cause,
	}
}

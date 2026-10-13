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

	// ErrSharedKeyRevocation indicates the blast-radius guard (WithSharedKeyGuard)
	// blocked an erasure because a key to revoke also protects events for other
	// subjects (e.g. a per-tenant key). Set AllowSharedKeyRevocation to proceed.
	ErrSharedKeyRevocation = errors.New("mink: erasure would revoke a key shared with other subjects (blast-radius guard); set AllowSharedKeyRevocation to proceed")

	// ErrResidualCountUnsupported is returned by a SubjectResidualCounter whose
	// underlying store cannot count the rows attributable to a subject — it lacks the
	// optional adapters counter extension (SubjectOutboxCounter, SubjectAuditCounter,
	// SubjectIdempotencyCounter, SubjectSagaCounter). DataEraser.Verify and the
	// erasure certificate record such a store under UncheckedStores / StoresUnchecked
	// rather than failing: the store is neither proven clean nor proven dirty.
	ErrResidualCountUnsupported = errors.New("mink: subject store cannot count residual rows (counter extension not implemented)")
)

// SharedKeyError reports that erasing the target subject would revoke one or more
// keys that also protect other subjects' events — the per-tenant-key blast radius.
// It is returned by Erase (before any revocation) when WithSharedKeyGuard is set and
// AllowSharedKeyRevocation is not.
type SharedKeyError struct {
	SubjectID string
	// SharedKeys are the keys that also protect other (or untagged) subjects' events.
	//
	// It is for PROGRAMMATIC use only (e.g. to decide whether to re-run with
	// AllowSharedKeyRevocation, or to split the keys). Under a per-subject key resolver
	// (WithSubjectKeyResolver) a key id embeds the subject id it was derived from — for a
	// shared event the FIRST tagged subject's — so a key id can itself name a co-subject.
	// Error() therefore prints only the NUMBER of shared keys; do not log or serialize
	// this field into audit trails.
	SharedKeys []string
	// OtherSubjects samples subject ids (other than the target) found under those keys.
	//
	// It is for PROGRAMMATIC use only — e.g. to decide whether to re-run with
	// AllowSharedKeyRevocation, or to notify an operator through a secured channel.
	// It is deliberately NOT included in Error(): an error message is routinely
	// logged, and a log line must never disclose other data subjects' identifiers.
	// Do not log or serialize this field into audit trails.
	OtherSubjects []string
	// OtherSubjectCount is the total number of distinct other subjects found under
	// the shared keys. OtherSubjects is a bounded sample, so this can exceed its
	// length. Zero with a non-empty SharedKeys means the keys are shared only with
	// untagged (ownership-unprovable) events.
	OtherSubjectCount int
}

// Error returns the error message. It names the target subject and gives the NUMBER of
// shared keys and the NUMBER of other subjects affected — never the other subjects'
// identifiers, and never the key ids (which can embed a co-subject's id under a
// per-subject key resolver). SharedKeys and OtherSubjects remain available on the typed
// error for programmatic use.
func (e *SharedKeyError) Error() string {
	n := e.OtherSubjectCount
	if n < len(e.OtherSubjects) {
		n = len(e.OtherSubjects)
	}
	return fmt.Sprintf("mink: erasing subject %q would revoke %d key(s) shared with %d other subject(s) and/or untagged events (blast-radius guard); set AllowSharedKeyRevocation to proceed",
		e.SubjectID, len(e.SharedKeys), n)
}

// Is reports whether this error matches the target error.
func (e *SharedKeyError) Is(target error) bool {
	return target == ErrSharedKeyRevocation
}

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

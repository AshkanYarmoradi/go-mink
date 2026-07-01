package mink

import "context"

// SubjectErasable is an OPTIONAL seam letting stores that hold PII *derived* from
// events — the audit trail, saga state, snapshots, or an external sink — be erased
// alongside crypto-shredding. Register implementations via the WithSubjectStore option
// of NewDataEraser; Erase invokes each after key revocation and read-model
// redaction, records a SubjectErasureOutcome, and treats a per-store failure as
// non-fatal (symmetric with WithErasureHook).
//
// Implementations MUST target READ-SIDE stores only, so subject-scoped deletion never
// violates the append-only event log. Use the built-in NewAuditSubjectEraser /
// NewSagaSubjectEraser / NewSnapshotSubjectEraser, or supply your own.
type SubjectErasable interface {
	// EraseSubject removes the subject's data from the store. footprint carries the
	// subject's resolved streams and revoked keys (e.g. a snapshot eraser keys off
	// footprint.Streams). It returns an outcome (count erased, or Skipped when the
	// store cannot target the subject) and a non-nil error only on a real failure.
	EraseSubject(ctx context.Context, subjectID string, footprint *SubjectFootprint) (SubjectErasureOutcome, error)

	// ErasableName identifies the store in ErasureResult (no PII), e.g. "audit".
	ErasableName() string
}

// SubjectErasureOutcome reports what one SubjectErasable did for a subject. It carries
// no PII and is safe to record on an ErasureResult / certificate.
type SubjectErasureOutcome struct {
	// Name identifies the store (e.g. "audit", "saga", "snapshot").
	Name string `json:"name"`

	// Erased is the number of rows/records removed for the subject.
	Erased int `json:"erased"`

	// Skipped is true when the store could not target the subject (e.g. the
	// underlying adapter does not implement the optional purger sub-interface).
	Skipped bool `json:"skipped,omitempty"`

	// Err holds a non-fatal failure message, if any.
	Err string `json:"error,omitempty"`
}

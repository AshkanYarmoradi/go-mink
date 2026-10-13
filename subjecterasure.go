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
// NewSagaSubjectEraser / NewSnapshotSubjectEraser, or supply your own. Implement
// SubjectResidualCounter as well so DataEraser.Verify and the erasure certificate can
// prove the store is clean instead of certifying from the event log alone.
type SubjectErasable interface {
	// EraseSubject removes the subject's data from the store. footprint carries the
	// subject's resolved streams (with the ones shared with other subjects listed under
	// SharedStreams) and revoked keys; e.g. a snapshot eraser keys off footprint.Streams,
	// and a store that keys rows by stream id should purge only
	// footprint.ExclusiveStreams() (see SubjectFootprintIDs) and report the rest in
	// SubjectErasureOutcome.SharedStreamsSkipped. It returns an outcome (count erased, or
	// Skipped when the store cannot target the subject) and a non-nil error only on a
	// real failure.
	EraseSubject(ctx context.Context, subjectID string, footprint *SubjectFootprint) (SubjectErasureOutcome, error)

	// ErasableName identifies the store in ErasureResult (no PII), e.g. "audit".
	ErasableName() string
}

// SubjectResidualCounter is an OPTIONAL extension of SubjectErasable that lets
// DataEraser.Verify — and the ErasureCertificate emitted by Erase — prove a sibling
// store holds NO row attributable to the subject. Without it, verification reads the
// event log alone, which never sees the plaintext copies an audit trail, saga state,
// outbox or snapshot keeps.
//
// The built-in erasers (NewAuditSubjectEraser, NewSagaSubjectEraser,
// NewOutboxSubjectEraser, NewIdempotencySubjectEraser, NewSnapshotSubjectEraser)
// implement it. The store-backed ones count through the store's optional counter
// extension (SubjectOutboxCounter, SubjectAuditCounter, SubjectIdempotencyCounter,
// SubjectSagaCounter) over the same id set the purge used (see SubjectFootprintIDs),
// and return ErrResidualCountUnsupported when the underlying store lacks it. A
// SubjectErasable that does not implement this interface, or that returns
// ErrResidualCountUnsupported, is reported under VerificationReport.UncheckedStores /
// ErasureCertificate.StoresUnchecked — neither proven clean nor dirty — instead of
// being silently certified.
type SubjectResidualCounter interface {
	// CountSubjectResidual returns the number of rows still attributable to subjectID
	// in the store, given the subject's resolved footprint (the same streams / keys
	// EraseSubject received; may be nil). Zero means the store is clean for this
	// subject. Any other error than ErrResidualCountUnsupported is a real failure.
	CountSubjectResidual(ctx context.Context, subjectID string, footprint *SubjectFootprint) (int64, error)
}

// SubjectStoreResidual reports rows still attributable to a subject in one registered
// sibling store, as found through SubjectResidualCounter during verification. It
// carries no PII (store name and a count only).
type SubjectStoreResidual struct {
	// Name identifies the store (ErasableName), e.g. "outbox".
	Name string `json:"name"`
	// Count is the number of rows still attributable to the subject.
	Count int64 `json:"count"`
}

// SubjectErasureOutcome reports what one SubjectErasable did for a subject. It carries
// no PII and is safe to record on an ErasureResult / certificate.
type SubjectErasureOutcome struct {
	// Name identifies the store (e.g. "audit", "saga", "snapshot").
	Name string `json:"name"`

	// Erased is the number of rows/records removed for the subject. It is int64 to match
	// the purger APIs' row counts (e.g. sql.Result.RowsAffected), so large purges are
	// reported without truncation or overflow.
	Erased int64 `json:"erased"`

	// Skipped is true when the store could not target the subject (e.g. the
	// underlying adapter does not implement the optional purger sub-interface).
	Skipped bool `json:"skipped,omitempty"`

	// FootprintAware is true when the store was purged by the subject's RESOLVED
	// footprint — the bare subject id plus its EXCLUSIVE stream ids (and, with
	// WithDerivedAggregateIDs, the aggregate ids derived from them; see
	// SubjectFootprintIDs) — rather than by bare subject-id equality alone.
	// Library-written rows are keyed by the footprint (an outbox AggregateID is the
	// producing STREAM id such as "User-u1"; audit / idempotency AggregateIDs hold the
	// targeted aggregate id; a saga CorrelationID is a business id), so only the
	// footprint path reaches them. On a built-in eraser a false value means the store
	// implements only the legacy adapters.Subject*Purger and such rows were NOT reached.
	FootprintAware bool `json:"footprintAware,omitempty"`

	// SharedStreamsSkipped is the number of footprint streams the store's footprint
	// purge left untouched because they are shared with other subjects
	// (SubjectFootprint.SharedStreams): rows keyed by such a stream id — a co-tenant's
	// pending outbox messages, an in-flight saga, another subject's audit or idempotency
	// rows — cannot be attributed to this subject alone, so they are neither deleted nor
	// counted. Rows of the subject on those streams MAY remain; ErasureResult.Notes says
	// so and the erasure certificate is not Verified while it is non-zero. Zero on the
	// legacy (bare-subject-id) path, which never keys by stream, and for stores whose
	// per-stream rows hold no co-tenant data (snapshots are deleted on every footprint
	// stream: a snapshot is a rebuildable cache of plaintext state).
	SharedStreamsSkipped int `json:"sharedStreamsSkipped,omitempty"`

	// Err holds a non-fatal failure message, if any.
	Err string `json:"error,omitempty"`
}

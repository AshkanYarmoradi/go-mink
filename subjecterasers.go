package mink

import (
	"context"

	"go-mink.dev/adapters"
)

// NewAuditSubjectEraser wraps an AuditStore as a SubjectErasable so DataEraser reaches
// the subject's audit trail (Article 17). The audit log records who/what/when in
// plaintext (actor, tenant, arbitrary metadata, raw error strings that can carry PII),
// which crypto-shredding the events does NOT touch. If the store implements the optional
// adapters.SubjectAuditPurger, EraseSubject deletes the subject's rows; otherwise it
// reports Skipped (never fails). Register with DataEraser.WithSubjectStore.
func NewAuditSubjectEraser(store AuditStore) SubjectErasable {
	return &auditSubjectEraser{store: store}
}

type auditSubjectEraser struct{ store AuditStore }

func (a *auditSubjectEraser) ErasableName() string { return "audit" }

func (a *auditSubjectEraser) EraseSubject(ctx context.Context, subjectID string, _ *SubjectFootprint) (SubjectErasureOutcome, error) {
	purger, ok := a.store.(SubjectAuditPurger)
	if !ok {
		return SubjectErasureOutcome{Name: a.ErasableName(), Skipped: true}, nil
	}
	n, err := purger.DeleteAuditBySubject(ctx, subjectID)
	if err != nil {
		return SubjectErasureOutcome{Name: a.ErasableName()}, err
	}
	return SubjectErasureOutcome{Name: a.ErasableName(), Erased: n}, nil
}

// NewSagaSubjectEraser wraps a SagaStore as a SubjectErasable so DataEraser reaches the
// subject's saga state. Sagas copy correlation/business data out of events into their
// own plaintext state, which crypto-shredding does NOT touch. If the store implements
// the optional adapters.SubjectSagaPurger, EraseSubject deletes sagas whose
// CorrelationID equals the subject id; otherwise it reports Skipped. Register with
// DataEraser.WithSubjectStore.
func NewSagaSubjectEraser(store SagaStore) SubjectErasable {
	return &sagaSubjectEraser{store: store}
}

type sagaSubjectEraser struct{ store SagaStore }

func (s *sagaSubjectEraser) ErasableName() string { return "saga" }

func (s *sagaSubjectEraser) EraseSubject(ctx context.Context, subjectID string, _ *SubjectFootprint) (SubjectErasureOutcome, error) {
	purger, ok := s.store.(SubjectSagaPurger)
	if !ok {
		return SubjectErasureOutcome{Name: s.ErasableName(), Skipped: true}, nil
	}
	n, err := purger.DeleteSagasBySubject(ctx, subjectID)
	if err != nil {
		return SubjectErasureOutcome{Name: s.ErasableName()}, err
	}
	return SubjectErasureOutcome{Name: s.ErasableName(), Erased: n}, nil
}

// NewOutboxSubjectEraser wraps an OutboxStore as a SubjectErasable so DataEraser reaches
// the subject's outbox rows. The default outbox path stores the ENCRYPTED payload (which
// crypto-shredding erases), but a route Transform that emits a decrypted/reshaped payload
// leaves an independent plaintext copy, and dead-lettered rows persist. If the store
// implements the optional adapters.SubjectOutboxPurger, EraseSubject deletes rows whose
// AggregateID equals the subject id; otherwise it reports Skipped. Register with
// DataEraser.WithSubjectStore.
func NewOutboxSubjectEraser(store OutboxStore) SubjectErasable {
	return &outboxSubjectEraser{store: store}
}

type outboxSubjectEraser struct{ store OutboxStore }

func (o *outboxSubjectEraser) ErasableName() string { return "outbox" }

func (o *outboxSubjectEraser) EraseSubject(ctx context.Context, subjectID string, _ *SubjectFootprint) (SubjectErasureOutcome, error) {
	purger, ok := o.store.(SubjectOutboxPurger)
	if !ok {
		return SubjectErasureOutcome{Name: o.ErasableName(), Skipped: true}, nil
	}
	n, err := purger.DeleteOutboxBySubject(ctx, subjectID)
	if err != nil {
		return SubjectErasureOutcome{Name: o.ErasableName()}, err
	}
	return SubjectErasureOutcome{Name: o.ErasableName(), Erased: n}, nil
}

// NewIdempotencySubjectEraser wraps an IdempotencyStore as a SubjectErasable so DataEraser
// reaches the subject's idempotency records. Records are TTL-bounded and keyed by a command
// hash, but the optional Response payload can hold PII. If the store implements the optional
// adapters.SubjectIdempotencyPurger, EraseSubject deletes records whose AggregateID equals
// the subject id; otherwise it reports Skipped. Register with DataEraser.WithSubjectStore.
func NewIdempotencySubjectEraser(store IdempotencyStore) SubjectErasable {
	return &idempotencySubjectEraser{store: store}
}

type idempotencySubjectEraser struct{ store IdempotencyStore }

func (i *idempotencySubjectEraser) ErasableName() string { return "idempotency" }

func (i *idempotencySubjectEraser) EraseSubject(ctx context.Context, subjectID string, _ *SubjectFootprint) (SubjectErasureOutcome, error) {
	purger, ok := i.store.(SubjectIdempotencyPurger)
	if !ok {
		return SubjectErasureOutcome{Name: i.ErasableName(), Skipped: true}, nil
	}
	n, err := purger.DeleteIdempotencyBySubject(ctx, subjectID)
	if err != nil {
		return SubjectErasureOutcome{Name: i.ErasableName()}, err
	}
	return SubjectErasureOutcome{Name: i.ErasableName(), Erased: n}, nil
}

// NewSnapshotSubjectEraser wraps a SnapshotAdapter as a SubjectErasable that deletes the
// snapshot of each stream in the subject's resolved footprint. Snapshots serialize
// decrypted aggregate STATE in plaintext, which crypto-shredding does NOT touch, so an
// un-deleted snapshot leaves the subject's PII recoverable. DeleteSnapshot is idempotent,
// so Erased counts the footprint streams whose snapshot was cleared. Register with
// DataEraser.WithSubjectStore.
func NewSnapshotSubjectEraser(adapter adapters.SnapshotAdapter) SubjectErasable {
	return &snapshotSubjectEraser{adapter: adapter}
}

type snapshotSubjectEraser struct{ adapter adapters.SnapshotAdapter }

func (s *snapshotSubjectEraser) ErasableName() string { return "snapshot" }

func (s *snapshotSubjectEraser) EraseSubject(ctx context.Context, _ string, fp *SubjectFootprint) (SubjectErasureOutcome, error) {
	if fp == nil || len(fp.Streams) == 0 {
		return SubjectErasureOutcome{Name: s.ErasableName()}, nil
	}
	var erased int64
	var firstErr error
	for _, streamID := range fp.Streams {
		if err := s.adapter.DeleteSnapshot(ctx, streamID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		erased++
	}
	return SubjectErasureOutcome{Name: s.ErasableName(), Erased: erased}, firstErr
}

package memory

import (
	"context"

	"go-mink.dev/adapters"
)

// Ensure the footprint-aware GDPR extensions are implemented at compile time.
var (
	_ adapters.SubjectOutboxFootprintPurger      = (*OutboxStore)(nil)
	_ adapters.SubjectOutboxCounter              = (*OutboxStore)(nil)
	_ adapters.SubjectAuditFootprintPurger       = (*AuditStore)(nil)
	_ adapters.SubjectAuditCounter               = (*AuditStore)(nil)
	_ adapters.SubjectIdempotencyFootprintPurger = (*IdempotencyStore)(nil)
	_ adapters.SubjectIdempotencyCounter         = (*IdempotencyStore)(nil)
	_ adapters.SubjectSagaFootprintPurger        = (*SagaStore)(nil)
	_ adapters.SubjectSagaCounter                = (*SagaStore)(nil)
	_ adapters.SagaCorrelationTypeFinder         = (*SagaStore)(nil)
)

// idSet de-duplicates ids into a lookup set, dropping empty strings. It returns
// nil when nothing remains so callers can short-circuit with (0, nil) without
// taking the store's lock.
func idSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// ---------------------------------------------------------------------------
// OutboxStore
// ---------------------------------------------------------------------------

// DeleteOutboxByAggregateIDs removes outbox messages whose AggregateID equals any of
// aggregateIDs and returns the count removed. The outbox's AggregateID holds the
// producing STREAM id, so the mink eraser passes the subject's resolved footprint
// streams. An empty slice returns (0, nil) without touching the store. Implements
// adapters.SubjectOutboxFootprintPurger.
func (s *OutboxStore) DeleteOutboxByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error) {
	set := idSet(aggregateIDs)
	if set == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var count int64
	for id, msg := range s.messages {
		if _, ok := set[msg.AggregateID]; ok {
			delete(s.messages, id)
			count++
		}
	}
	return count, nil
}

// CountOutboxByAggregateIDs returns the number of outbox messages whose AggregateID
// equals any of aggregateIDs (the producing STREAM ids). An empty slice returns
// (0, nil) without touching the store. Implements adapters.SubjectOutboxCounter.
func (s *OutboxStore) CountOutboxByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error) {
	set := idSet(aggregateIDs)
	if set == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int64
	for _, msg := range s.messages {
		if _, ok := set[msg.AggregateID]; ok {
			count++
		}
	}
	return count, nil
}

// ---------------------------------------------------------------------------
// AuditStore
// ---------------------------------------------------------------------------

// DeleteAuditByAggregateIDs removes audit entries whose AggregateID equals any of
// aggregateIDs (the STREAM ids the audited commands targeted) and returns the count
// removed. An empty slice returns (0, nil) without touching the store. Implements
// adapters.SubjectAuditFootprintPurger.
func (s *AuditStore) DeleteAuditByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error) {
	set := idSet(aggregateIDs)
	if set == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	kept := make([]*adapters.AuditEntry, 0, len(s.entries))
	var removed int64
	for _, entry := range s.entries {
		if _, ok := set[entry.AggregateID]; ok {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	s.entries = kept
	return removed, nil
}

// CountAuditBySubject returns the number of audit entries whose Actor equals
// subjectID OR whose AggregateID equals any of aggregateIDs (the subject's resolved
// footprint STREAM ids). An empty subjectID disables the Actor predicate and an
// empty slice disables the AggregateID predicate; when both are empty it returns
// (0, nil) without touching the store. Implements adapters.SubjectAuditCounter.
func (s *AuditStore) CountAuditBySubject(ctx context.Context, subjectID string, aggregateIDs []string) (int64, error) {
	set := idSet(aggregateIDs)
	if subjectID == "" && set == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int64
	for _, entry := range s.entries {
		if subjectID != "" && entry.Actor == subjectID {
			count++
			continue
		}
		if _, ok := set[entry.AggregateID]; ok {
			count++
		}
	}
	return count, nil
}

// ---------------------------------------------------------------------------
// IdempotencyStore
// ---------------------------------------------------------------------------

// DeleteIdempotencyByAggregateIDs removes idempotency records whose AggregateID
// equals any of aggregateIDs (the STREAM ids the commands affected) and returns the
// count removed. An empty slice returns (0, nil) without touching the store.
// Implements adapters.SubjectIdempotencyFootprintPurger.
func (s *IdempotencyStore) DeleteIdempotencyByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error) {
	set := idSet(aggregateIDs)
	if set == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var count int64
	for key, rec := range s.records {
		if _, ok := set[rec.AggregateID]; ok {
			delete(s.records, key)
			count++
		}
	}
	return count, nil
}

// CountIdempotencyByAggregateIDs returns the number of idempotency records whose
// AggregateID equals any of aggregateIDs. An empty slice returns (0, nil) without
// touching the store. Implements adapters.SubjectIdempotencyCounter.
func (s *IdempotencyStore) CountIdempotencyByAggregateIDs(ctx context.Context, aggregateIDs []string) (int64, error) {
	set := idSet(aggregateIDs)
	if set == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int64
	for _, rec := range s.records {
		if _, ok := set[rec.AggregateID]; ok {
			count++
		}
	}
	return count, nil
}

// ---------------------------------------------------------------------------
// SagaStore
// ---------------------------------------------------------------------------

// DeleteSagasByCorrelationIDs removes saga states whose CorrelationID equals any of
// correlationIDs and returns the count removed. An empty slice returns (0, nil)
// without touching the store. Implements adapters.SubjectSagaFootprintPurger.
func (s *SagaStore) DeleteSagasByCorrelationIDs(ctx context.Context, correlationIDs []string) (int64, error) {
	set := idSet(correlationIDs)
	if set == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var removed int64
	for id, state := range s.sagas {
		if _, ok := set[state.CorrelationID]; ok {
			delete(s.sagas, id)
			removed++
		}
	}
	return removed, nil
}

// CountSagasByCorrelationIDs returns the number of saga states whose CorrelationID
// equals any of correlationIDs. An empty slice returns (0, nil) without touching the
// store. Implements adapters.SubjectSagaCounter.
func (s *SagaStore) CountSagasByCorrelationIDs(ctx context.Context, correlationIDs []string) (int64, error) {
	set := idSet(correlationIDs)
	if set == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int64
	for _, state := range s.sagas {
		if _, ok := set[state.CorrelationID]; ok {
			count++
		}
	}
	return count, nil
}

// FindByCorrelationIDAndType returns a deep copy of the most recently started saga
// whose CorrelationID equals correlationID AND whose Type equals sagaType. Unlike
// FindByCorrelationID it never returns a saga of a different type that happens to
// share the correlation id, so a type-B saga cannot hydrate from (and overwrite) a
// type-A entry. Returns a SagaNotFoundError (errors.Is ErrSagaNotFound) when no such
// saga exists. Implements adapters.SagaCorrelationTypeFinder.
func (s *SagaStore) FindByCorrelationIDAndType(ctx context.Context, correlationID, sagaType string) (*adapters.SagaState, error) {
	// ErrEmptyStreamID is reused for a missing identifier; see Save for rationale.
	if correlationID == "" || sagaType == "" {
		return nil, adapters.ErrEmptyStreamID
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var latest *adapters.SagaState
	for _, state := range s.sagas {
		if state.CorrelationID != correlationID || state.Type != sagaType {
			continue
		}
		if latest == nil || state.StartedAt.After(latest.StartedAt) {
			latest = state
		}
	}
	if latest == nil {
		return nil, &adapters.SagaNotFoundError{CorrelationID: correlationID}
	}
	return s.copyState(latest), nil
}

package memory

import (
	"context"
	"sync"
	"time"

	"go-mink.dev/adapters"
)

// Ensure interface compliance at compile time
var (
	_ adapters.SagaStore         = (*SagaStore)(nil)
	_ adapters.SubjectSagaPurger = (*SagaStore)(nil)
)

// SagaStore provides an in-memory implementation of adapters.SagaStore.
// This is primarily intended for testing and development purposes.
type SagaStore struct {
	mu    sync.RWMutex
	sagas map[string]*adapters.SagaState
}

// NewSagaStore creates a new in-memory SagaStore.
func NewSagaStore() *SagaStore {
	return &SagaStore{
		sagas: make(map[string]*adapters.SagaState),
	}
}

// Save persists a saga state with optimistic concurrency control.
//
// Version semantics:
//   - Version 0: Creates a new saga. If a saga with this ID already exists,
//     it returns a ConcurrencyError.
//   - Version > 0: Updates an existing saga. If no saga exists with this ID,
//     it returns SagaNotFoundError. If the version doesn't match, it returns
//     ConcurrencyError.
//
// After a successful save, state.Version is incremented to reflect the new version.
//
// Reject-on-missing contract: a Version > 0 save against a missing saga is
// rejected with SagaNotFoundError rather than silently creating the saga. This
// is intentional and is the stricter of the two possible behaviors: a non-zero
// version is a claim that a prior version already exists, so creating one here
// would mask a lost-write or an out-of-order Save. Callers that mean to create
// a saga must start from Version 0. Note this is intentionally stricter than the
// PostgreSQL adapter, whose upsert-style Save may create the row in this case;
// code that must behave identically across adapters should always create with
// Version 0 first.
func (s *SagaStore) Save(ctx context.Context, state *adapters.SagaState) error {
	if state == nil {
		return adapters.ErrNilAggregate
	}

	// Note: there is no dedicated "empty saga ID" sentinel in the adapters
	// package, so ErrEmptyStreamID is reused here (and throughout this store)
	// to signal a missing required saga identifier — the saga ID is the saga's
	// stream identifier. This matches the PostgreSQL saga store's validation.
	if state.ID == "" {
		return adapters.ErrEmptyStreamID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	existing, exists := s.sagas[state.ID]

	// Version check for optimistic concurrency
	if exists {
		if state.Version != existing.Version {
			return &adapters.ConcurrencyError{
				StreamID:        state.ID,
				ExpectedVersion: state.Version,
				ActualVersion:   existing.Version,
			}
		}
	} else if state.Version > 0 {
		// New saga but version > 0 indicates expected existing saga
		return &adapters.SagaNotFoundError{SagaID: state.ID}
	}

	// Create a deep copy to prevent external mutation
	savedState := &adapters.SagaState{
		ID:            state.ID,
		Type:          state.Type,
		CorrelationID: state.CorrelationID,
		Status:        state.Status,
		CurrentStep:   state.CurrentStep,
		FailureReason: state.FailureReason,
		StartedAt:     state.StartedAt,
		UpdatedAt:     time.Now(),
		Version:       state.Version + 1,
	}

	// Copy CompletedAt if set
	if state.CompletedAt != nil {
		completedAt := *state.CompletedAt
		savedState.CompletedAt = &completedAt
	}

	// Deep copy Data (handles nested maps and slices)
	if state.Data != nil {
		savedState.Data = deepCopyMap(state.Data)
	}

	// Deep copy ProcessedEvents
	if state.ProcessedEvents != nil {
		savedState.ProcessedEvents = make([]string, len(state.ProcessedEvents))
		copy(savedState.ProcessedEvents, state.ProcessedEvents)
	}

	// Deep copy Steps
	if state.Steps != nil {
		savedState.Steps = make([]adapters.SagaStep, len(state.Steps))
		copy(savedState.Steps, state.Steps)
	}

	s.sagas[state.ID] = savedState

	// Update the original state's version
	state.Version = savedState.Version

	return nil
}

// Load retrieves a saga state by ID.
func (s *SagaStore) Load(ctx context.Context, sagaID string) (*adapters.SagaState, error) {
	// ErrEmptyStreamID is reused for a missing saga ID; see Save for rationale.
	if sagaID == "" {
		return nil, adapters.ErrEmptyStreamID
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	state, exists := s.sagas[sagaID]
	if !exists {
		return nil, &adapters.SagaNotFoundError{SagaID: sagaID}
	}

	// Return a deep copy
	return s.copyState(state), nil
}

// FindByCorrelationID finds a saga by its correlation ID.
func (s *SagaStore) FindByCorrelationID(ctx context.Context, correlationID string) (*adapters.SagaState, error) {
	// ErrEmptyStreamID is reused for a missing correlation ID; see Save for rationale.
	if correlationID == "" {
		return nil, adapters.ErrEmptyStreamID
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Find the most recently started saga with this correlation ID
	var latestSaga *adapters.SagaState
	for _, state := range s.sagas {
		if state.CorrelationID == correlationID {
			if latestSaga == nil || state.StartedAt.After(latestSaga.StartedAt) {
				latestSaga = state
			}
		}
	}

	if latestSaga == nil {
		return nil, &adapters.SagaNotFoundError{CorrelationID: correlationID}
	}

	return s.copyState(latestSaga), nil
}

// FindByType finds all sagas of a given type with the specified statuses.
func (s *SagaStore) FindByType(ctx context.Context, sagaType string, statuses ...adapters.SagaStatus) ([]*adapters.SagaState, error) {
	// ErrEmptyStreamID is reused for a missing saga type; see Save for rationale.
	if sagaType == "" {
		return nil, adapters.ErrEmptyStreamID
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var result []*adapters.SagaState
	statusSet := make(map[adapters.SagaStatus]bool, len(statuses))
	for _, status := range statuses {
		statusSet[status] = true
	}

	for _, state := range s.sagas {
		if state.Type != sagaType {
			continue
		}

		// If no statuses specified, include all
		if len(statuses) == 0 || statusSet[state.Status] {
			result = append(result, s.copyState(state))
		}
	}

	return result, nil
}

// Delete removes a saga state.
func (s *SagaStore) Delete(ctx context.Context, sagaID string) error {
	// ErrEmptyStreamID is reused for a missing saga ID; see Save for rationale.
	if sagaID == "" {
		return adapters.ErrEmptyStreamID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if _, exists := s.sagas[sagaID]; !exists {
		return &adapters.SagaNotFoundError{SagaID: sagaID}
	}

	delete(s.sagas, sagaID)
	return nil
}

// DeleteSagasBySubject removes saga states whose CorrelationID equals subjectID, for
// GDPR erasure of saga state that copied the subject's PII out of events. Returns the
// number removed. Implements adapters.SubjectSagaPurger.
func (s *SagaStore) DeleteSagasBySubject(ctx context.Context, subjectID string) (int64, error) {
	if subjectID == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var removed int64
	for id, state := range s.sagas {
		if state.CorrelationID == subjectID {
			delete(s.sagas, id)
			removed++
		}
	}
	return removed, nil
}

// Close releases any resources (no-op for in-memory implementation).
func (s *SagaStore) Close() error {
	return nil
}

// Clear removes all sagas (useful for testing).
func (s *SagaStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sagas = make(map[string]*adapters.SagaState)
}

// Count returns the total number of sagas stored.
func (s *SagaStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sagas)
}

// CountByStatus returns the count of sagas by status.
func (s *SagaStore) CountByStatus(ctx context.Context) (map[adapters.SagaStatus]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	counts := make(map[adapters.SagaStatus]int64)
	for _, state := range s.sagas {
		counts[state.Status]++
	}

	return counts, nil
}

// All returns all sagas (useful for testing).
func (s *SagaStore) All() []*adapters.SagaState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*adapters.SagaState, 0, len(s.sagas))
	for _, state := range s.sagas {
		result = append(result, s.copyState(state))
	}

	return result
}

// copyState creates a deep copy of a SagaState.
func (s *SagaStore) copyState(state *adapters.SagaState) *adapters.SagaState {
	copied := &adapters.SagaState{
		ID:            state.ID,
		Type:          state.Type,
		CorrelationID: state.CorrelationID,
		Status:        state.Status,
		CurrentStep:   state.CurrentStep,
		FailureReason: state.FailureReason,
		StartedAt:     state.StartedAt,
		UpdatedAt:     state.UpdatedAt,
		Version:       state.Version,
	}

	// Copy CompletedAt if set
	if state.CompletedAt != nil {
		completedAt := *state.CompletedAt
		copied.CompletedAt = &completedAt
	}

	// Deep copy Data (handles nested maps and slices)
	if state.Data != nil {
		copied.Data = deepCopyMap(state.Data)
	}

	// Deep copy ProcessedEvents
	if state.ProcessedEvents != nil {
		copied.ProcessedEvents = make([]string, len(state.ProcessedEvents))
		copy(copied.ProcessedEvents, state.ProcessedEvents)
	}

	// Deep copy Steps
	if state.Steps != nil {
		copied.Steps = make([]adapters.SagaStep, len(state.Steps))
		copy(copied.Steps, state.Steps)
	}

	return copied
}

// deepCopyMap creates a deep copy of a map[string]interface{}.
func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = deepCopyValue(v)
	}
	return result
}

// deepCopyValue creates a deep copy of a value, handling maps and slices recursively.
func deepCopyValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case map[string]interface{}:
		return deepCopyMap(val)
	case []interface{}:
		return deepCopySlice(val)
	default:
		// Primitive types (string, int, float64, bool, etc.) are copied by value
		return v
	}
}

// deepCopySlice creates a deep copy of a []interface{}.
func deepCopySlice(s []interface{}) []interface{} {
	if s == nil {
		return nil
	}
	result := make([]interface{}, len(s))
	for i, v := range s {
		result[i] = deepCopyValue(v)
	}
	return result
}

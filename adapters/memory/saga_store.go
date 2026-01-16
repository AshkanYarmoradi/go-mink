package memory

import (
	"context"
	"sync"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Ensure interface compliance at compile time
var _ adapters.SagaStore = (*SagaStore)(nil)

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

// Save persists a saga state.
// Uses optimistic concurrency control based on the Version field.
func (s *SagaStore) Save(ctx context.Context, state *adapters.SagaState) error {
	if state == nil {
		return adapters.ErrNilAggregate
	}

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

	// Deep copy Data
	if state.Data != nil {
		savedState.Data = make(map[string]interface{}, len(state.Data))
		for k, v := range state.Data {
			savedState.Data[k] = v
		}
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

	// Deep copy Data
	if state.Data != nil {
		copied.Data = make(map[string]interface{}, len(state.Data))
		for k, v := range state.Data {
			copied.Data[k] = v
		}
	}

	// Deep copy Steps
	if state.Steps != nil {
		copied.Steps = make([]adapters.SagaStep, len(state.Steps))
		copy(copied.Steps, state.Steps)
	}

	return copied
}

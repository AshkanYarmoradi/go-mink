package memory

import (
	"context"
	"sync"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Ensure interface compliance at compile time.
var _ adapters.CheckpointAdapter = (*CheckpointStore)(nil)

// Checkpoint represents a stored checkpoint.
type Checkpoint struct {
	ProjectionName string
	Position       uint64
	UpdatedAt      time.Time
}

// CheckpointStore provides an in-memory implementation of CheckpointAdapter.
type CheckpointStore struct {
	mu          sync.RWMutex
	checkpoints map[string]*Checkpoint
}

// NewCheckpointStore creates a new in-memory checkpoint store.
func NewCheckpointStore() *CheckpointStore {
	return &CheckpointStore{
		checkpoints: make(map[string]*Checkpoint),
	}
}

// GetCheckpoint retrieves the current position for a projection.
// Returns 0 if no checkpoint exists.
func (s *CheckpointStore) GetCheckpoint(ctx context.Context, projectionName string) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if cp, ok := s.checkpoints[projectionName]; ok {
		return cp.Position, nil
	}
	return 0, nil
}

// SetCheckpoint stores the position for a projection.
func (s *CheckpointStore) SetCheckpoint(ctx context.Context, projectionName string, position uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.checkpoints[projectionName] = &Checkpoint{
		ProjectionName: projectionName,
		Position:       position,
		UpdatedAt:      time.Now(),
	}
	return nil
}

// DeleteCheckpoint removes a checkpoint for a projection.
func (s *CheckpointStore) DeleteCheckpoint(ctx context.Context, projectionName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.checkpoints, projectionName)
	return nil
}

// GetAllCheckpoints returns all stored checkpoints.
func (s *CheckpointStore) GetAllCheckpoints(ctx context.Context) (map[string]uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]uint64, len(s.checkpoints))
	for name, cp := range s.checkpoints {
		result[name] = cp.Position
	}
	return result, nil
}

// Clear removes all checkpoints.
func (s *CheckpointStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.checkpoints = make(map[string]*Checkpoint)
}

// Len returns the number of checkpoints.
func (s *CheckpointStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.checkpoints)
}

// GetCheckpointWithTimestamp retrieves the checkpoint with its last update time.
func (s *CheckpointStore) GetCheckpointWithTimestamp(ctx context.Context, projectionName string) (*Checkpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if cp, ok := s.checkpoints[projectionName]; ok {
		return &Checkpoint{
			ProjectionName: cp.ProjectionName,
			Position:       cp.Position,
			UpdatedAt:      cp.UpdatedAt,
		}, nil
	}
	return nil, nil
}

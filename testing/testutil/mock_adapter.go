// Package testutil provides test utilities and fixtures for testing go-mink applications.
package testutil

import (
	"context"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// MockAdapter is a mock implementation of adapters.EventStoreAdapter for testing.
type MockAdapter struct {
	AppendErr           error
	LoadErr             error
	GetStreamInfoErr    error
	GetLastPositionErr  error
	LoadFromPositionErr error
	Events              []adapters.StoredEvent
}

// Append implements adapters.EventStoreAdapter.
func (m *MockAdapter) Append(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
	if m.AppendErr != nil {
		return nil, m.AppendErr
	}
	stored := make([]adapters.StoredEvent, len(events))
	for i, e := range events {
		stored[i] = adapters.StoredEvent{
			ID:             "event-" + e.Type,
			StreamID:       streamID,
			Type:           e.Type,
			Data:           e.Data,
			Metadata:       e.Metadata,
			Version:        int64(i + 1),
			GlobalPosition: uint64(i + 1),
			Timestamp:      time.Now(),
		}
	}
	return stored, nil
}

// Load implements adapters.EventStoreAdapter.
func (m *MockAdapter) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	if m.LoadErr != nil {
		return nil, m.LoadErr
	}
	return m.Events, nil
}

// GetStreamInfo implements adapters.EventStoreAdapter.
func (m *MockAdapter) GetStreamInfo(ctx context.Context, streamID string) (*adapters.StreamInfo, error) {
	if m.GetStreamInfoErr != nil {
		return nil, m.GetStreamInfoErr
	}
	return &adapters.StreamInfo{
		StreamID:   streamID,
		Version:    int64(len(m.Events)),
		EventCount: int64(len(m.Events)),
	}, nil
}

// GetLastPosition implements adapters.EventStoreAdapter.
func (m *MockAdapter) GetLastPosition(ctx context.Context) (uint64, error) {
	if m.GetLastPositionErr != nil {
		return 0, m.GetLastPositionErr
	}
	if len(m.Events) == 0 {
		return 0, nil
	}
	return m.Events[len(m.Events)-1].GlobalPosition, nil
}

// Initialize implements adapters.EventStoreAdapter.
func (m *MockAdapter) Initialize(ctx context.Context) error {
	return nil
}

// Close implements adapters.EventStoreAdapter.
func (m *MockAdapter) Close() error {
	return nil
}

// ============================================================================
// SubscriptionAdapter implementation
// ============================================================================

// LoadFromPosition implements adapters.SubscriptionAdapter.
func (m *MockAdapter) LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]adapters.StoredEvent, error) {
	if m.LoadFromPositionErr != nil {
		return nil, m.LoadFromPositionErr
	}
	return m.Events, nil
}

// SubscribeAll implements adapters.SubscriptionAdapter.
func (m *MockAdapter) SubscribeAll(ctx context.Context, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	ch := make(chan adapters.StoredEvent)
	go func() {
		defer close(ch)
		for _, e := range m.Events {
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// SubscribeStream implements adapters.SubscriptionAdapter.
func (m *MockAdapter) SubscribeStream(ctx context.Context, streamID string, fromVersion int64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	ch := make(chan adapters.StoredEvent)
	go func() {
		defer close(ch)
		for _, e := range m.Events {
			if e.StreamID == streamID {
				select {
				case ch <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}

// SubscribeCategory implements adapters.SubscriptionAdapter.
func (m *MockAdapter) SubscribeCategory(ctx context.Context, category string, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	ch := make(chan adapters.StoredEvent)
	go func() {
		defer close(ch)
		for _, e := range m.Events {
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// Ensure MockAdapter implements adapters.EventStoreAdapter.
var _ adapters.EventStoreAdapter = (*MockAdapter)(nil)

// Ensure MockAdapter implements adapters.SubscriptionAdapter.
var _ adapters.SubscriptionAdapter = (*MockAdapter)(nil)

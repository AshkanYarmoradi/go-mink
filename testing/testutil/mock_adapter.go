// Package testutil provides test utilities and fixtures for testing go-mink applications.
package testutil

import (
	"context"
	"strconv"
	"sync"
	"time"

	"go-mink.dev/adapters"
)

// MockAdapter is a mock implementation of adapters.EventStoreAdapter for testing.
//
// It keeps real per-stream version and global-position counters so sequential
// Appends produce monotonic, distinct versions/positions and optimistic
// concurrency checks behave like the production memory adapter. The injectable
// *Err fields let tests force a specific method to fail. All shared state is
// guarded by mu so the adapter is safe for concurrent use.
type MockAdapter struct {
	AppendErr           error
	LoadErr             error
	GetStreamInfoErr    error
	GetLastPositionErr  error
	LoadFromPositionErr error

	mu             sync.Mutex
	Events         []adapters.StoredEvent
	streamVersions map[string]int64
	globalPosition uint64
}

// Append implements adapters.EventStoreAdapter.
//
// It honors expectedVersion using the same optimistic-concurrency semantics as
// the real adapters (AnyVersion=-1 skips the check, NoStream=0 requires the
// stream not to exist, StreamExists=-2 requires it to exist, and a concrete
// version must match the current stream version). On a mismatch it returns an
// adapters.ConcurrencyError (which satisfies errors.Is(err,
// adapters.ErrConcurrencyConflict)). The appended events are assigned monotonic
// per-stream versions and global positions and recorded in m.Events.
func (m *MockAdapter) Append(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
	if m.AppendErr != nil {
		return nil, m.AppendErr
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.streamVersions == nil {
		m.streamVersions = make(map[string]int64)
	}

	currentVersion, exists := m.streamVersions[streamID]
	if err := adapters.CheckVersion(streamID, expectedVersion, currentVersion, exists); err != nil {
		return nil, err
	}

	stored := make([]adapters.StoredEvent, len(events))
	for i, e := range events {
		currentVersion++
		m.globalPosition++
		stored[i] = adapters.StoredEvent{
			ID:             "event-" + strconv.FormatUint(m.globalPosition, 10),
			StreamID:       streamID,
			Type:           e.Type,
			Data:           e.Data,
			Metadata:       e.Metadata,
			Version:        currentVersion,
			GlobalPosition: m.globalPosition,
			Timestamp:      time.Now(),
		}
	}

	m.streamVersions[streamID] = currentVersion
	m.Events = append(m.Events, stored...)
	return stored, nil
}

// Load implements adapters.EventStoreAdapter. It returns the events for the
// given stream whose version is greater than fromVersion.
//
// As a convenience for tests that populate m.Events directly with minimal
// fixtures, an event with an empty StreamID matches any requested stream, and an
// event with Version 0 is always included. Events created via Append carry real
// StreamIDs and versions and so filter precisely.
func (m *MockAdapter) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	if m.LoadErr != nil {
		return nil, m.LoadErr
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var events []adapters.StoredEvent
	for _, e := range m.Events {
		streamMatches := e.StreamID == streamID || e.StreamID == ""
		versionMatches := e.Version == 0 || e.Version > fromVersion
		if streamMatches && versionMatches {
			events = append(events, e)
		}
	}
	return events, nil
}

// GetStreamInfo implements adapters.EventStoreAdapter.
func (m *MockAdapter) GetStreamInfo(ctx context.Context, streamID string) (*adapters.StreamInfo, error) {
	if m.GetStreamInfoErr != nil {
		return nil, m.GetStreamInfoErr
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var count, version int64
	for _, e := range m.Events {
		// Empty StreamID matches any requested stream (minimal-fixture convenience).
		if e.StreamID == streamID || e.StreamID == "" {
			count++
			if e.Version > version {
				version = e.Version
			}
		}
	}
	// Honor both Append-tracked versions and events populated directly on m.Events.
	if v := m.streamVersions[streamID]; v > version {
		version = v
	}
	return &adapters.StreamInfo{
		StreamID:   streamID,
		Version:    version,
		EventCount: count,
	}, nil
}

// GetLastPosition implements adapters.EventStoreAdapter.
func (m *MockAdapter) GetLastPosition(ctx context.Context) (uint64, error) {
	if m.GetLastPositionErr != nil {
		return 0, m.GetLastPositionErr
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Honor both the Append-tracked counter and events populated directly on
	// m.Events (tests commonly set Events without calling Append).
	last := m.globalPosition
	for _, e := range m.Events {
		if e.GlobalPosition > last {
			last = e.GlobalPosition
		}
	}
	return last, nil
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
//
// It returns only events whose GlobalPosition is greater than fromPosition,
// capped at limit when limit > 0. This matches the real adapter contract so
// paginating scanners advance past consumed events instead of looping forever.
func (m *MockAdapter) LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]adapters.StoredEvent, error) {
	if m.LoadFromPositionErr != nil {
		return nil, m.LoadFromPositionErr
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var events []adapters.StoredEvent
	for _, e := range m.Events {
		if e.GlobalPosition > fromPosition {
			events = append(events, e)
			if limit > 0 && len(events) >= limit {
				break
			}
		}
	}
	return events, nil
}

// snapshotEvents returns a copy of the current events under lock so the
// Subscribe* goroutines can range over a stable slice without holding the mutex
// while sending on the channel.
func (m *MockAdapter) snapshotEvents() []adapters.StoredEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]adapters.StoredEvent, len(m.Events))
	copy(out, m.Events)
	return out
}

// SubscribeAll implements adapters.SubscriptionAdapter.
func (m *MockAdapter) SubscribeAll(ctx context.Context, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	events := m.snapshotEvents()
	ch := make(chan adapters.StoredEvent)
	go func() {
		defer close(ch)
		for _, e := range events {
			if e.GlobalPosition <= fromPosition {
				continue
			}
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
	events := m.snapshotEvents()
	ch := make(chan adapters.StoredEvent)
	go func() {
		defer close(ch)
		for _, e := range events {
			if e.StreamID == streamID && e.Version > fromVersion {
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
	events := m.snapshotEvents()
	ch := make(chan adapters.StoredEvent)
	go func() {
		defer close(ch)
		for _, e := range events {
			if e.GlobalPosition <= fromPosition {
				continue
			}
			if adapters.ExtractCategory(e.StreamID) != category {
				continue
			}
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

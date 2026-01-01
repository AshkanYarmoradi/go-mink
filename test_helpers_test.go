package mink

// test_helpers.go contains shared test doubles and utilities for mink package tests.
// These types are only compiled during testing.

import (
	"context"
	"sync"
	"time"
)

// =============================================================================
// Shared Test Logger
// =============================================================================

// testLogger is a shared test implementation of Logger.
type testLogger struct {
	mu        sync.Mutex
	debugLogs []string
	infoLogs  []string
	warnLogs  []string
	errorLogs []string
}

func newTestLogger() *testLogger {
	return &testLogger{}
}

func (l *testLogger) Debug(msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.debugLogs = append(l.debugLogs, msg)
}

func (l *testLogger) Info(msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infoLogs = append(l.infoLogs, msg)
}

func (l *testLogger) Warn(msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnLogs = append(l.warnLogs, msg)
}

func (l *testLogger) Error(msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errorLogs = append(l.errorLogs, msg)
}

// =============================================================================
// Shared Test CheckpointStore
// =============================================================================

// testCheckpointStore is a shared test implementation of CheckpointStore.
type testCheckpointStore struct {
	mu          sync.RWMutex
	checkpoints map[string]uint64
	setErr      error
	getErr      error
	deleteErr   error
}

func newTestCheckpointStore() *testCheckpointStore {
	return &testCheckpointStore{
		checkpoints: make(map[string]uint64),
	}
}

func (s *testCheckpointStore) GetCheckpoint(ctx context.Context, projectionName string) (uint64, error) {
	if s.getErr != nil {
		return 0, s.getErr
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.checkpoints[projectionName], nil
}

func (s *testCheckpointStore) SetCheckpoint(ctx context.Context, projectionName string, position uint64) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints[projectionName] = position
	return nil
}

func (s *testCheckpointStore) DeleteCheckpoint(ctx context.Context, projectionName string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.checkpoints, projectionName)
	return nil
}

func (s *testCheckpointStore) GetAllCheckpoints(ctx context.Context) (map[string]uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]uint64, len(s.checkpoints))
	for k, v := range s.checkpoints {
		result[k] = v
	}
	return result, nil
}

// =============================================================================
// Shared Test ProjectionMetrics
// =============================================================================

// testProjectionMetrics is a shared test implementation of ProjectionMetrics.
type testProjectionMetrics struct {
	mu               sync.Mutex
	eventsProcessed  int
	batchesProcessed int
	checkpointsSet   int
	errorsRecorded   int
	lastProjection   string
	lastEventType    string
	lastDuration     time.Duration
	lastSuccess      bool
}

func newTestProjectionMetrics() *testProjectionMetrics {
	return &testProjectionMetrics{}
}

func (m *testProjectionMetrics) RecordEventProcessed(projectionName, eventType string, duration time.Duration, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventsProcessed++
	m.lastProjection = projectionName
	m.lastEventType = eventType
	m.lastDuration = duration
	m.lastSuccess = success
}

func (m *testProjectionMetrics) RecordBatchProcessed(projectionName string, count int, duration time.Duration, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchesProcessed++
	m.lastProjection = projectionName
	m.lastSuccess = success
}

func (m *testProjectionMetrics) RecordCheckpoint(projectionName string, position uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkpointsSet++
	m.lastProjection = projectionName
}

func (m *testProjectionMetrics) RecordError(projectionName string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errorsRecorded++
	m.lastProjection = projectionName
}

// =============================================================================
// Shared Test Inline Projection
// =============================================================================

// testInlineProjection is a shared test implementation of InlineProjection.
type testInlineProjection struct {
	ProjectionBase
	mu       sync.Mutex
	events   []StoredEvent
	applyErr error
}

func newTestInlineProjection(name string, handledEvents ...string) *testInlineProjection {
	return &testInlineProjection{
		ProjectionBase: NewProjectionBase(name, handledEvents...),
		events:         make([]StoredEvent, 0),
	}
}

func (p *testInlineProjection) Apply(ctx context.Context, event StoredEvent) error {
	if p.applyErr != nil {
		return p.applyErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *testInlineProjection) Events() []StoredEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]StoredEvent, len(p.events))
	copy(result, p.events)
	return result
}

func (p *testInlineProjection) SetError(err error) {
	p.applyErr = err
}

// =============================================================================
// Shared Test Async Projection
// =============================================================================

// testAsyncProjection is a shared test implementation of AsyncProjection.
type testAsyncProjection struct {
	AsyncProjectionBase
	mu            sync.Mutex
	events        []StoredEvent
	applyErr      error
	batchApplyErr error
	supportsBatch bool
}

func newTestAsyncProjection(name string, handledEvents ...string) *testAsyncProjection {
	return &testAsyncProjection{
		AsyncProjectionBase: NewAsyncProjectionBase(name, handledEvents...),
		events:              make([]StoredEvent, 0),
		supportsBatch:       false,
	}
}

func (p *testAsyncProjection) Apply(ctx context.Context, event StoredEvent) error {
	if p.applyErr != nil {
		return p.applyErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *testAsyncProjection) ApplyBatch(ctx context.Context, events []StoredEvent) error {
	if !p.supportsBatch {
		return ErrNotImplemented
	}
	if p.batchApplyErr != nil {
		return p.batchApplyErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, events...)
	return nil
}

func (p *testAsyncProjection) Events() []StoredEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]StoredEvent, len(p.events))
	copy(result, p.events)
	return result
}

func (p *testAsyncProjection) SetError(err error) {
	p.applyErr = err
}

func (p *testAsyncProjection) EnableBatch() {
	p.supportsBatch = true
}

// =============================================================================
// Shared Test Live Projection
// =============================================================================

// testLiveProjection is a shared test implementation of LiveProjection.
type testLiveProjection struct {
	LiveProjectionBase
	mu        sync.Mutex
	events    []StoredEvent
	eventChan chan StoredEvent
}

func newTestLiveProjection(name string, isTransient bool, handledEvents ...string) *testLiveProjection {
	return &testLiveProjection{
		LiveProjectionBase: NewLiveProjectionBase(name, isTransient, handledEvents...),
		events:             make([]StoredEvent, 0),
		eventChan:          make(chan StoredEvent, 100),
	}
}

func (p *testLiveProjection) OnEvent(ctx context.Context, event StoredEvent) {
	p.mu.Lock()
	p.events = append(p.events, event)
	p.mu.Unlock()

	// Non-blocking send to event channel
	select {
	case p.eventChan <- event:
	default:
	}
}

func (p *testLiveProjection) Events() []StoredEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]StoredEvent, len(p.events))
	copy(result, p.events)
	return result
}

func (p *testLiveProjection) WaitForEvent(timeout time.Duration) (StoredEvent, bool) {
	select {
	case event := <-p.eventChan:
		return event, true
	case <-time.After(timeout):
		return StoredEvent{}, false
	}
}

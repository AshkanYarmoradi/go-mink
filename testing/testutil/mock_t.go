// Package testutil provides test utilities and fixtures for testing go-mink applications.
package testutil

import (
	"runtime"
	"testing"
)

// MockT is a mock testing.TB that captures test failures for testing.
// It is used to test functions that call testing.T methods like Fatal, Error, etc.
type MockT struct {
	testing.TB // embed to satisfy unexported methods
	Failed_    bool
	Fatal_     bool
	Message    string
	Logs       []string
}

// NewMockT creates a new MockT instance.
func NewMockT() *MockT {
	return &MockT{Logs: make([]string, 0)}
}

// Helper implements testing.TB.
func (m *MockT) Helper() {}

// Error implements testing.TB.
func (m *MockT) Error(args ...any) {
	m.Failed_ = true
	if len(args) > 0 {
		if msg, ok := args[0].(string); ok {
			m.Message = msg
		}
	}
}

// Errorf implements testing.TB.
func (m *MockT) Errorf(format string, args ...any) {
	m.Failed_ = true
	m.Message = format
}

// Fail implements testing.TB.
func (m *MockT) Fail() { m.Failed_ = true }

// FailNow implements testing.TB.
func (m *MockT) FailNow() {
	m.Failed_ = true
	runtime.Goexit()
}

// Failed implements testing.TB.
func (m *MockT) Failed() bool { return m.Failed_ }

// Fatal implements testing.TB.
func (m *MockT) Fatal(args ...any) {
	m.Failed_ = true
	m.Fatal_ = true
	if len(args) > 0 {
		if msg, ok := args[0].(string); ok {
			m.Message = msg
		}
	}
	runtime.Goexit()
}

// Fatalf implements testing.TB.
func (m *MockT) Fatalf(format string, args ...any) {
	m.Failed_ = true
	m.Fatal_ = true
	m.Message = format
	runtime.Goexit()
}

// RunWithMockT runs a function with a MockT and waits for completion.
// This handles runtime.Goexit() calls from Fatal/FailNow.
func RunWithMockT(fn func(m *MockT)) *MockT {
	mt := NewMockT()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(mt)
	}()
	<-done
	return mt
}

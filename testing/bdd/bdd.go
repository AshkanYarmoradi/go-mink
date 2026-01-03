// Package bdd provides BDD-style test fixtures for event-sourced aggregates.
// It enables expressive Given-When-Then testing patterns for command handling
// and aggregate behavior verification.
package bdd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AshkanYarmoradi/go-mink"
)

// TB is an alias for testing.TB interface to allow mocking in tests
type TB = testing.TB

// TestFixture provides BDD-style testing for aggregates.
type TestFixture struct {
	t           TB
	aggregate   mink.Aggregate
	givenEvents []interface{}
	result      error
	executed    bool
}

// Given sets up the initial aggregate with optional historical events.
// This establishes the "Given" state before executing a command.
func Given(t TB, aggregate mink.Aggregate, events ...interface{}) *TestFixture {
	t.Helper()
	return &TestFixture{
		t:           t,
		aggregate:   aggregate,
		givenEvents: events,
	}
}

// When executes a command function against the aggregate.
// The command function should call methods on the aggregate and return any error.
func (f *TestFixture) When(commandFunc func() error) *TestFixture {
	f.t.Helper()

	// Apply given events to establish initial state
	for _, event := range f.givenEvents {
		if err := f.aggregate.ApplyEvent(event); err != nil {
			f.t.Fatalf("Failed to apply given event %T: %v", event, err)
		}
	}
	f.aggregate.ClearUncommittedEvents()

	// Execute the command
	f.result = commandFunc()
	f.executed = true

	return f
}

// Then asserts that the aggregate produced the expected events.
func (f *TestFixture) Then(expectedEvents ...interface{}) {
	f.t.Helper()

	if !f.executed {
		f.t.Fatal("bdd: Then() must be called after When() - no command was executed")
	}

	if f.result != nil {
		f.t.Fatalf("Expected success but got error: %v", f.result)
	}

	uncommitted := f.aggregate.UncommittedEvents()
	if len(uncommitted) != len(expectedEvents) {
		f.t.Fatalf("Expected %d events, got %d.\nExpected: %+v\nActual: %+v",
			len(expectedEvents), len(uncommitted), expectedEvents, uncommitted)
	}

	for i, expected := range expectedEvents {
		if !reflect.DeepEqual(uncommitted[i], expected) {
			f.t.Errorf("Event %d mismatch:\nExpected: %+v\nActual: %+v",
				i, expected, uncommitted[i])
		}
	}
}

// ThenError asserts that the command produced the expected error.
func (f *TestFixture) ThenError(expectedErr error) {
	f.t.Helper()

	if !f.executed {
		f.t.Fatal("bdd: ThenError() must be called after When() - no command was executed")
	}

	if f.result == nil {
		f.t.Fatal("Expected error but got success")
	}

	if !errors.Is(f.result, expectedErr) {
		f.t.Errorf("Expected error %v, got %v", expectedErr, f.result)
	}
}

// ThenErrorContains asserts that the error message contains a substring.
func (f *TestFixture) ThenErrorContains(substring string) {
	f.t.Helper()

	if !f.executed {
		f.t.Fatal("bdd: ThenErrorContains() must be called after When() - no command was executed")
	}

	if f.result == nil {
		f.t.Fatal("Expected error but got success")
	}

	if !strings.Contains(f.result.Error(), substring) {
		f.t.Errorf("Expected error containing %q, got %q", substring, f.result.Error())
	}
}

// ThenNoEvents asserts that no events were produced.
func (f *TestFixture) ThenNoEvents() {
	f.t.Helper()

	if !f.executed {
		f.t.Fatal("bdd: ThenNoEvents() must be called after When() - no command was executed")
	}

	if f.result != nil {
		f.t.Fatalf("Expected success but got error: %v", f.result)
	}

	uncommitted := f.aggregate.UncommittedEvents()
	if len(uncommitted) > 0 {
		f.t.Errorf("Expected no events, got %d: %+v", len(uncommitted), uncommitted)
	}
}

// CommandTestFixture provides BDD-style testing with command bus integration.
type CommandTestFixture struct {
	t           TB
	ctx         context.Context
	bus         *mink.CommandBus
	store       *mink.EventStore
	givenEvents []struct {
		streamID string
		event    interface{}
	}
	result   mink.CommandResult
	err      error
	executed bool
}

// GivenCommand creates a new command test fixture with a command bus.
func GivenCommand(t TB, bus *mink.CommandBus, store *mink.EventStore) *CommandTestFixture {
	t.Helper()
	return &CommandTestFixture{
		t:     t,
		ctx:   context.Background(),
		bus:   bus,
		store: store,
	}
}

// WithContext sets a custom context for the command execution.
func (f *CommandTestFixture) WithContext(ctx context.Context) *CommandTestFixture {
	f.ctx = ctx
	return f
}

// WithExistingEvents sets up initial events in the event store.
func (f *CommandTestFixture) WithExistingEvents(streamID string, events ...interface{}) *CommandTestFixture {
	for _, event := range events {
		f.givenEvents = append(f.givenEvents, struct {
			streamID string
			event    interface{}
		}{streamID: streamID, event: event})
	}
	return f
}

// When dispatches the command.
func (f *CommandTestFixture) When(cmd mink.Command) *CommandTestFixture {
	f.t.Helper()

	// Store existing events if store is provided
	if f.store != nil {
		for _, ge := range f.givenEvents {
			err := f.store.Append(f.ctx, ge.streamID, []interface{}{ge.event})
			if err != nil {
				f.t.Fatalf("Failed to store given event: %v", err)
			}
		}
	}

	f.result, f.err = f.bus.Dispatch(f.ctx, cmd)
	f.executed = true
	return f
}

// ThenSucceeds asserts the command succeeded.
func (f *CommandTestFixture) ThenSucceeds() *CommandTestFixture {
	f.t.Helper()

	if !f.executed {
		f.t.Fatal("bdd: ThenSucceeds() must be called after When() - no command was dispatched")
	}

	if f.err != nil {
		f.t.Fatalf("Expected success but got error: %v", f.err)
	}

	if !f.result.IsSuccess() {
		f.t.Fatalf("Expected success result but got error: %v", f.result.Error)
	}

	return f
}

// ThenFails asserts the command failed with the expected error.
func (f *CommandTestFixture) ThenFails(expectedErr error) {
	f.t.Helper()

	if !f.executed {
		f.t.Fatal("bdd: ThenFails() must be called after When() - no command was dispatched")
	}

	if f.err == nil && f.result.IsSuccess() {
		f.t.Fatal("Expected failure but got success")
	}

	errToCheck := f.err
	if errToCheck == nil {
		errToCheck = f.result.Error
	}

	if !errors.Is(errToCheck, expectedErr) {
		f.t.Errorf("Expected error %v, got %v", expectedErr, errToCheck)
	}
}

// ThenReturnsAggregateID asserts the result contains the expected aggregate ID.
func (f *CommandTestFixture) ThenReturnsAggregateID(expected string) *CommandTestFixture {
	f.t.Helper()

	if !f.executed {
		f.t.Fatal("bdd: ThenReturnsAggregateID() must be called after When() - no command was dispatched")
	}

	if f.result.AggregateID != expected {
		f.t.Errorf("Expected aggregate ID %q, got %q", expected, f.result.AggregateID)
	}

	return f
}

// ThenReturnsVersion asserts the result contains the expected version.
func (f *CommandTestFixture) ThenReturnsVersion(expected int64) *CommandTestFixture {
	f.t.Helper()

	if !f.executed {
		f.t.Fatal("bdd: ThenReturnsVersion() must be called after When() - no command was dispatched")
	}

	if f.result.Version != expected {
		f.t.Errorf("Expected version %d, got %d", expected, f.result.Version)
	}

	return f
}

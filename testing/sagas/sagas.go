// Package sagas provides testing utilities for saga (process manager) development.
// It includes fixtures for testing saga state transitions, event handling,
// and command generation.
package sagas

import (
	"context"
	"reflect"
	"testing"

	"github.com/AshkanYarmoradi/go-mink"
)

// TB is an alias for testing.TB to enable easier mocking in tests.
type TB = testing.TB

// Saga represents a saga or process manager interface.
// Sagas coordinate long-running processes by listening to events and
// dispatching commands.
type Saga interface {
	// Name returns the saga's unique identifier.
	Name() string

	// HandleEvent processes an event and returns commands to dispatch.
	HandleEvent(ctx context.Context, event mink.StoredEvent) ([]mink.Command, error)

	// IsComplete returns true if the saga has completed.
	IsComplete() bool

	// State returns the current saga state.
	State() interface{}
}

// SagaTestFixture provides BDD-style testing for sagas.
type SagaTestFixture struct {
	t        TB
	ctx      context.Context
	saga     Saga
	commands []mink.Command
	err      error
}

// TestSaga creates a new saga test fixture.
func TestSaga(t TB, saga Saga) *SagaTestFixture {
	t.Helper()
	return &SagaTestFixture{
		t:        t,
		ctx:      context.Background(),
		saga:     saga,
		commands: make([]mink.Command, 0),
	}
}

// WithContext sets a custom context for event handling.
func (f *SagaTestFixture) WithContext(ctx context.Context) *SagaTestFixture {
	f.ctx = ctx
	return f
}

// GivenEvents applies events to the saga and collects commands.
func (f *SagaTestFixture) GivenEvents(events ...mink.StoredEvent) *SagaTestFixture {
	f.t.Helper()

	for _, event := range events {
		cmds, err := f.saga.HandleEvent(f.ctx, event)
		if err != nil {
			f.err = err
			return f
		}
		f.commands = append(f.commands, cmds...)
	}

	return f
}

// ThenCommands asserts that the saga produced the expected commands.
func (f *SagaTestFixture) ThenCommands(expected ...mink.Command) *SagaTestFixture {
	f.t.Helper()

	if f.err != nil {
		f.t.Fatalf("Saga returned error: %v", f.err)
	}

	if len(f.commands) != len(expected) {
		f.t.Fatalf("Expected %d commands, got %d.\nExpected: %+v\nActual: %+v",
			len(expected), len(f.commands), expected, f.commands)
	}

	for i, exp := range expected {
		if !reflect.DeepEqual(f.commands[i], exp) {
			f.t.Errorf("Command %d mismatch:\nExpected: %+v\nActual: %+v",
				i, exp, f.commands[i])
		}
	}

	return f
}

// ThenNoCommands asserts that no commands were produced.
func (f *SagaTestFixture) ThenNoCommands() *SagaTestFixture {
	f.t.Helper()

	if f.err != nil {
		f.t.Fatalf("Saga returned error: %v", f.err)
	}

	if len(f.commands) > 0 {
		f.t.Errorf("Expected no commands, got %d: %+v", len(f.commands), f.commands)
	}

	return f
}

// ThenCompleted asserts that the saga has completed.
func (f *SagaTestFixture) ThenCompleted() *SagaTestFixture {
	f.t.Helper()

	if !f.saga.IsComplete() {
		f.t.Error("Expected saga to be complete, but it is not")
	}

	return f
}

// ThenNotCompleted asserts that the saga has not completed.
func (f *SagaTestFixture) ThenNotCompleted() *SagaTestFixture {
	f.t.Helper()

	if f.saga.IsComplete() {
		f.t.Error("Expected saga to not be complete, but it is")
	}

	return f
}

// ThenState asserts the saga's current state.
func (f *SagaTestFixture) ThenState(expected interface{}) *SagaTestFixture {
	f.t.Helper()

	actual := f.saga.State()
	if !reflect.DeepEqual(actual, expected) {
		f.t.Errorf("State mismatch:\nExpected: %+v\nActual: %+v", expected, actual)
	}

	return f
}

// ThenError asserts that an error occurred during event handling.
func (f *SagaTestFixture) ThenError(expected error) *SagaTestFixture {
	f.t.Helper()

	if f.err == nil {
		f.t.Fatal("Expected error but got success")
	}

	if expected != nil && f.err.Error() != expected.Error() {
		f.t.Errorf("Expected error %v, got %v", expected, f.err)
	}

	return f
}

// ThenCommandCount asserts the number of commands produced.
func (f *SagaTestFixture) ThenCommandCount(expected int) *SagaTestFixture {
	f.t.Helper()

	if len(f.commands) != expected {
		f.t.Errorf("Expected %d commands, got %d", expected, len(f.commands))
	}

	return f
}

// ThenFirstCommand asserts the first command matches.
func (f *SagaTestFixture) ThenFirstCommand(expected mink.Command) *SagaTestFixture {
	f.t.Helper()

	if len(f.commands) == 0 {
		f.t.Fatal("Expected at least one command, got none")
	}

	if !reflect.DeepEqual(f.commands[0], expected) {
		f.t.Errorf("First command mismatch:\nExpected: %+v\nActual: %+v",
			expected, f.commands[0])
	}

	return f
}

// ThenLastCommand asserts the last command matches.
func (f *SagaTestFixture) ThenLastCommand(expected mink.Command) *SagaTestFixture {
	f.t.Helper()

	if len(f.commands) == 0 {
		f.t.Fatal("Expected at least one command, got none")
	}

	last := f.commands[len(f.commands)-1]
	if !reflect.DeepEqual(last, expected) {
		f.t.Errorf("Last command mismatch:\nExpected: %+v\nActual: %+v",
			expected, last)
	}

	return f
}

// ThenContainsCommand asserts that a command exists in the produced commands.
func (f *SagaTestFixture) ThenContainsCommand(expected mink.Command) *SagaTestFixture {
	f.t.Helper()

	for _, cmd := range f.commands {
		if reflect.DeepEqual(cmd, expected) {
			return f
		}
	}

	f.t.Errorf("Commands do not contain expected command: %+v", expected)
	return f
}

// Commands returns the collected commands for additional assertions.
func (f *SagaTestFixture) Commands() []mink.Command {
	return f.commands
}

// Saga returns the underlying saga for additional assertions.
func (f *SagaTestFixture) Saga() Saga {
	return f.saga
}

// =============================================================================
// Saga State Machine Fixture
// =============================================================================

// SagaState represents the state of a saga.
type SagaState string

// Common saga states.
const (
	SagaStateNotStarted   SagaState = "NotStarted"
	SagaStateInProgress   SagaState = "InProgress"
	SagaStateCompleted    SagaState = "Completed"
	SagaStateFailed       SagaState = "Failed"
	SagaStateCompensating SagaState = "Compensating"
)

// StateTransition represents a state transition in a saga.
type StateTransition struct {
	From    SagaState
	To      SagaState
	Event   string
	Command string
}

// SagaStateMachineFixture tests saga state machine behavior.
type SagaStateMachineFixture struct {
	t           TB
	saga        Saga
	transitions []StateTransition
}

// TestSagaStateMachine creates a new state machine test fixture.
func TestSagaStateMachine(t TB, saga Saga) *SagaStateMachineFixture {
	t.Helper()
	return &SagaStateMachineFixture{
		t:           t,
		saga:        saga,
		transitions: make([]StateTransition, 0),
	}
}

// ExpectTransition defines an expected state transition.
func (f *SagaStateMachineFixture) ExpectTransition(from, to SagaState, event, command string) *SagaStateMachineFixture {
	f.transitions = append(f.transitions, StateTransition{
		From:    from,
		To:      to,
		Event:   event,
		Command: command,
	})
	return f
}

// Transitions returns the expected transitions for verification.
func (f *SagaStateMachineFixture) Transitions() []StateTransition {
	return f.transitions
}

// =============================================================================
// Compensation Test Fixture
// =============================================================================

// CompensationFixture tests saga compensation (rollback) behavior.
type CompensationFixture struct {
	t                    TB
	saga                 Saga
	ctx                  context.Context
	compensationCommands []mink.Command
}

// TestCompensation creates a new compensation test fixture.
func TestCompensation(t TB, saga Saga) *CompensationFixture {
	t.Helper()
	return &CompensationFixture{
		t:                    t,
		saga:                 saga,
		ctx:                  context.Background(),
		compensationCommands: make([]mink.Command, 0),
	}
}

// GivenFailureAfter simulates a failure after certain events.
func (f *CompensationFixture) GivenFailureAfter(events ...mink.StoredEvent) *CompensationFixture {
	f.t.Helper()

	for _, event := range events {
		_, err := f.saga.HandleEvent(f.ctx, event)
		if err != nil {
			// Failure occurred, collect compensation commands
			if compensator, ok := f.saga.(SagaCompensator); ok {
				f.compensationCommands = compensator.GetCompensationCommands()
			}
			break
		}
	}

	// Also check for compensation commands after processing all events
	// (some sagas set compensation commands without returning errors)
	if len(f.compensationCommands) == 0 {
		if compensator, ok := f.saga.(SagaCompensator); ok {
			f.compensationCommands = compensator.GetCompensationCommands()
		}
	}

	return f
}

// ThenCompensates asserts the expected compensation commands.
func (f *CompensationFixture) ThenCompensates(expected ...mink.Command) {
	f.t.Helper()

	if len(f.compensationCommands) != len(expected) {
		f.t.Fatalf("Expected %d compensation commands, got %d",
			len(expected), len(f.compensationCommands))
	}

	for i, exp := range expected {
		if !reflect.DeepEqual(f.compensationCommands[i], exp) {
			f.t.Errorf("Compensation command %d mismatch:\nExpected: %+v\nActual: %+v",
				i, exp, f.compensationCommands[i])
		}
	}
}

// SagaCompensator is implemented by sagas that support compensation.
type SagaCompensator interface {
	GetCompensationCommands() []mink.Command
}

// =============================================================================
// Timeout Test Fixture
// =============================================================================

// TimeoutFixture tests saga timeout behavior.
type TimeoutFixture struct {
	t    TB
	saga Saga
	ctx  context.Context
}

// TestTimeout creates a new timeout test fixture.
func TestTimeout(t TB, saga Saga) *TimeoutFixture {
	t.Helper()
	return &TimeoutFixture{
		t:    t,
		saga: saga,
		ctx:  context.Background(),
	}
}

// WithContext sets a custom context (can include timeout).
func (f *TimeoutFixture) WithContext(ctx context.Context) *TimeoutFixture {
	f.ctx = ctx
	return f
}

// SagaWithTimeout is implemented by sagas that support timeouts.
type SagaWithTimeout interface {
	Saga
	OnTimeout(ctx context.Context) ([]mink.Command, error)
}

// ThenOnTimeout simulates a timeout and checks commands.
func (f *TimeoutFixture) ThenOnTimeout(expected ...mink.Command) {
	f.t.Helper()

	timeoutSaga, ok := f.saga.(SagaWithTimeout)
	if !ok {
		f.t.Fatal("Saga does not implement SagaWithTimeout")
	}

	commands, err := timeoutSaga.OnTimeout(f.ctx)
	if err != nil {
		f.t.Fatalf("OnTimeout returned error: %v", err)
	}

	if len(commands) != len(expected) {
		f.t.Fatalf("Expected %d timeout commands, got %d",
			len(expected), len(commands))
	}

	for i, exp := range expected {
		if !reflect.DeepEqual(commands[i], exp) {
			f.t.Errorf("Timeout command %d mismatch:\nExpected: %+v\nActual: %+v",
				i, exp, commands[i])
		}
	}
}

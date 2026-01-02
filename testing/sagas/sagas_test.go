package sagas

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Mock testing.TB for testing failure cases
// =============================================================================

// mockT is a mock testing.TB that captures test failures for testing saga functions
type mockT struct {
	testing.TB // embed to satisfy unexported methods
	failed     bool
	fatal      bool
}

func newMockT() *mockT {
	return &mockT{}
}

func (m *mockT) Helper() {}

func (m *mockT) Error(args ...any)                 { m.failed = true }
func (m *mockT) Errorf(format string, args ...any) { m.failed = true }
func (m *mockT) Fail()                             { m.failed = true }
func (m *mockT) FailNow()                          { m.failed = true; runtime.Goexit() }
func (m *mockT) Failed() bool                      { return m.failed }
func (m *mockT) Fatal(args ...any)                 { m.failed = true; m.fatal = true; runtime.Goexit() }
func (m *mockT) Fatalf(format string, args ...any) { m.failed = true; m.fatal = true; runtime.Goexit() }

func runWithMockT(fn func(m *mockT)) *mockT {
	mt := newMockT()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(mt)
	}()
	<-done
	return mt
}

// =============================================================================
// Test Events
// =============================================================================

type TestOrderPlaced struct {
	OrderID    string
	CustomerID string
	Amount     float64
}

type TestPaymentReceived struct {
	OrderID   string
	PaymentID string
}

type TestPaymentFailed struct {
	OrderID string
	Reason  string
}

type TestInventoryReserved struct {
	OrderID string
}

type TestInventoryReservationFailed struct {
	OrderID string
	Reason  string
}

type TestShipmentScheduled struct {
	OrderID string
}

// =============================================================================
// Test Commands
// =============================================================================

type TestRequestPayment struct {
	mink.CommandBase
	OrderID string
	Amount  float64
}

func (c TestRequestPayment) CommandType() string { return "TestRequestPayment" }
func (c TestRequestPayment) Validate() error     { return nil }

type TestRefundPayment struct {
	mink.CommandBase
	OrderID   string
	PaymentID string
}

func (c TestRefundPayment) CommandType() string { return "TestRefundPayment" }
func (c TestRefundPayment) Validate() error     { return nil }

type TestReserveInventory struct {
	mink.CommandBase
	OrderID string
}

func (c TestReserveInventory) CommandType() string { return "TestReserveInventory" }
func (c TestReserveInventory) Validate() error     { return nil }

type TestReleaseInventory struct {
	mink.CommandBase
	OrderID string
}

func (c TestReleaseInventory) CommandType() string { return "TestReleaseInventory" }
func (c TestReleaseInventory) Validate() error     { return nil }

type TestScheduleShipment struct {
	mink.CommandBase
	OrderID string
}

func (c TestScheduleShipment) CommandType() string { return "TestScheduleShipment" }
func (c TestScheduleShipment) Validate() error     { return nil }

type TestCancelOrder struct {
	mink.CommandBase
	OrderID string
	Reason  string
}

func (c TestCancelOrder) CommandType() string { return "TestCancelOrder" }
func (c TestCancelOrder) Validate() error     { return nil }

// =============================================================================
// Test Saga Implementation
// =============================================================================

type testOrderFulfillmentSaga struct {
	id                   string
	state                SagaState
	orderID              string
	paymentID            string
	paymentReceived      bool
	inventoryReserved    bool
	shipmentScheduled    bool
	compensationCommands []mink.Command
}

func newTestOrderFulfillmentSaga(id string) *testOrderFulfillmentSaga {
	return &testOrderFulfillmentSaga{
		id:    id,
		state: SagaStateNotStarted,
	}
}

func (s *testOrderFulfillmentSaga) Name() string {
	return "OrderFulfillmentSaga"
}

func (s *testOrderFulfillmentSaga) HandleEvent(ctx context.Context, event mink.StoredEvent) ([]mink.Command, error) {
	switch event.Type {
	case "TestOrderPlaced":
		s.state = SagaStateInProgress
		s.orderID = "order-123" // Simplified
		return []mink.Command{
			TestRequestPayment{OrderID: s.orderID, Amount: 100.0},
		}, nil

	case "TestPaymentReceived":
		s.paymentReceived = true
		s.paymentID = "payment-123"
		return []mink.Command{
			TestReserveInventory{OrderID: s.orderID},
		}, nil

	case "TestPaymentFailed":
		s.state = SagaStateFailed
		return []mink.Command{
			TestCancelOrder{OrderID: s.orderID, Reason: "Payment failed"},
		}, nil

	case "TestInventoryReserved":
		s.inventoryReserved = true
		return []mink.Command{
			TestScheduleShipment{OrderID: s.orderID},
		}, nil

	case "TestInventoryReservationFailed":
		s.state = SagaStateCompensating
		s.compensationCommands = []mink.Command{
			TestRefundPayment{OrderID: s.orderID, PaymentID: s.paymentID},
		}
		return s.compensationCommands, nil

	case "TestShipmentScheduled":
		s.shipmentScheduled = true
		s.state = SagaStateCompleted
		return nil, nil
	}

	return nil, nil
}

func (s *testOrderFulfillmentSaga) IsComplete() bool {
	return s.state == SagaStateCompleted
}

func (s *testOrderFulfillmentSaga) State() interface{} {
	return s.state
}

func (s *testOrderFulfillmentSaga) GetCompensationCommands() []mink.Command {
	return s.compensationCommands
}

// =============================================================================
// TestSaga Tests
// =============================================================================

func TestTestSaga(t *testing.T) {
	t.Run("creates saga fixture", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")
		fixture := TestSaga(t, saga)

		assert.NotNil(t, fixture)
		assert.Equal(t, saga, fixture.saga)
	})
}

func TestSagaTestFixture_WithContext(t *testing.T) {
	t.Run("sets custom context", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")
		ctx := context.WithValue(context.Background(), "key", "value")

		fixture := TestSaga(t, saga).WithContext(ctx)

		assert.Equal(t, ctx, fixture.ctx)
	})
}

func TestSagaTestFixture_GivenEvents(t *testing.T) {
	t.Run("processes events and collects commands", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		fixture := TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"})

		assert.Len(t, fixture.commands, 1)
	})

	t.Run("processes multiple events", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		fixture := TestSaga(t, saga).
			GivenEvents(
				mink.StoredEvent{Type: "TestOrderPlaced"},
				mink.StoredEvent{Type: "TestPaymentReceived"},
			)

		assert.Len(t, fixture.commands, 2)
	})
}

func TestSagaTestFixture_ThenCommands(t *testing.T) {
	t.Run("passes when commands match", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
			ThenCommands(TestRequestPayment{OrderID: "order-123", Amount: 100.0})
	})

	t.Run("passes with multiple commands", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		TestSaga(t, saga).
			GivenEvents(
				mink.StoredEvent{Type: "TestOrderPlaced"},
				mink.StoredEvent{Type: "TestPaymentReceived"},
			).
			ThenCommands(
				TestRequestPayment{OrderID: "order-123", Amount: 100.0},
				TestReserveInventory{OrderID: "order-123"},
			)
	})
}

func TestSagaTestFixture_ThenNoCommands(t *testing.T) {
	t.Run("passes when no commands produced", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")
		saga.state = SagaStateInProgress
		saga.orderID = "order-123"
		saga.paymentReceived = true
		saga.inventoryReserved = true

		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestShipmentScheduled"}).
			ThenNoCommands()
	})
}

func TestSagaTestFixture_ThenCompleted(t *testing.T) {
	t.Run("passes when saga is complete", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")
		saga.state = SagaStateInProgress
		saga.orderID = "order-123"
		saga.paymentReceived = true
		saga.inventoryReserved = true

		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestShipmentScheduled"}).
			ThenCompleted()
	})
}

func TestSagaTestFixture_ThenNotCompleted(t *testing.T) {
	t.Run("passes when saga is not complete", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
			ThenNotCompleted()
	})
}

func TestSagaTestFixture_ThenState(t *testing.T) {
	t.Run("passes when state matches", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
			ThenState(SagaStateInProgress)
	})
}

func TestSagaTestFixture_ThenCommandCount(t *testing.T) {
	t.Run("passes when count matches", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		TestSaga(t, saga).
			GivenEvents(
				mink.StoredEvent{Type: "TestOrderPlaced"},
				mink.StoredEvent{Type: "TestPaymentReceived"},
			).
			ThenCommandCount(2)
	})
}

func TestSagaTestFixture_ThenFirstCommand(t *testing.T) {
	t.Run("passes when first command matches", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		TestSaga(t, saga).
			GivenEvents(
				mink.StoredEvent{Type: "TestOrderPlaced"},
				mink.StoredEvent{Type: "TestPaymentReceived"},
			).
			ThenFirstCommand(TestRequestPayment{OrderID: "order-123", Amount: 100.0})
	})
}

func TestSagaTestFixture_ThenLastCommand(t *testing.T) {
	t.Run("passes when last command matches", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		TestSaga(t, saga).
			GivenEvents(
				mink.StoredEvent{Type: "TestOrderPlaced"},
				mink.StoredEvent{Type: "TestPaymentReceived"},
			).
			ThenLastCommand(TestReserveInventory{OrderID: "order-123"})
	})
}

func TestSagaTestFixture_ThenContainsCommand(t *testing.T) {
	t.Run("passes when command exists", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		TestSaga(t, saga).
			GivenEvents(
				mink.StoredEvent{Type: "TestOrderPlaced"},
				mink.StoredEvent{Type: "TestPaymentReceived"},
			).
			ThenContainsCommand(TestReserveInventory{OrderID: "order-123"})
	})
}

func TestSagaTestFixture_Commands(t *testing.T) {
	t.Run("returns commands", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		fixture := TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"})

		assert.Len(t, fixture.Commands(), 1)
	})
}

func TestSagaTestFixture_Saga(t *testing.T) {
	t.Run("returns saga", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")
		fixture := TestSaga(t, saga)

		assert.Equal(t, saga, fixture.Saga())
	})
}

// =============================================================================
// State Machine Fixture Tests
// =============================================================================

func TestTestSagaStateMachine(t *testing.T) {
	t.Run("creates state machine fixture", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")
		fixture := TestSagaStateMachine(t, saga)

		assert.NotNil(t, fixture)
	})
}

func TestSagaStateMachineFixture_ExpectTransition(t *testing.T) {
	t.Run("records expected transitions", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		fixture := TestSagaStateMachine(t, saga).
			ExpectTransition(SagaStateNotStarted, SagaStateInProgress, "TestOrderPlaced", "TestRequestPayment").
			ExpectTransition(SagaStateInProgress, SagaStateCompleted, "TestShipmentScheduled", "")

		assert.Len(t, fixture.Transitions(), 2)
	})
}

// =============================================================================
// Compensation Fixture Tests
// =============================================================================

func TestTestCompensation(t *testing.T) {
	t.Run("creates compensation fixture", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")
		fixture := TestCompensation(t, saga)

		assert.NotNil(t, fixture)
	})
}

func TestCompensationFixture_GivenFailureAfter(t *testing.T) {
	t.Run("collects compensation commands on failure", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		// Setup the saga to be in a state where inventory reservation fails
		saga.state = SagaStateInProgress
		saga.orderID = "order-123"
		saga.paymentReceived = true
		saga.paymentID = "payment-123"

		fixture := TestCompensation(t, saga).
			GivenFailureAfter(mink.StoredEvent{Type: "TestInventoryReservationFailed"})

		assert.Len(t, fixture.compensationCommands, 1)
	})
}

func TestCompensationFixture_ThenCompensates(t *testing.T) {
	t.Run("passes when compensation commands match", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		saga.state = SagaStateInProgress
		saga.orderID = "order-123"
		saga.paymentReceived = true
		saga.paymentID = "payment-123"

		TestCompensation(t, saga).
			GivenFailureAfter(mink.StoredEvent{Type: "TestInventoryReservationFailed"}).
			ThenCompensates(TestRefundPayment{OrderID: "order-123", PaymentID: "payment-123"})
	})
}

// =============================================================================
// Timeout Fixture Tests
// =============================================================================

type testTimeoutSaga struct {
	testOrderFulfillmentSaga
}

func (s *testTimeoutSaga) OnTimeout(ctx context.Context) ([]mink.Command, error) {
	return []mink.Command{
		TestCancelOrder{OrderID: s.orderID, Reason: "Timeout"},
	}, nil
}

func TestTestTimeout(t *testing.T) {
	t.Run("creates timeout fixture", func(t *testing.T) {
		saga := &testTimeoutSaga{
			testOrderFulfillmentSaga: *newTestOrderFulfillmentSaga("saga-123"),
		}
		fixture := TestTimeout(t, saga)

		assert.NotNil(t, fixture)
	})
}

func TestTimeoutFixture_WithContext(t *testing.T) {
	t.Run("sets custom context", func(t *testing.T) {
		saga := &testTimeoutSaga{
			testOrderFulfillmentSaga: *newTestOrderFulfillmentSaga("saga-123"),
		}
		ctx := context.WithValue(context.Background(), "key", "value")

		fixture := TestTimeout(t, saga).WithContext(ctx)

		assert.Equal(t, ctx, fixture.ctx)
	})
}

func TestTimeoutFixture_ThenOnTimeout(t *testing.T) {
	t.Run("passes when timeout commands match", func(t *testing.T) {
		saga := &testTimeoutSaga{
			testOrderFulfillmentSaga: *newTestOrderFulfillmentSaga("saga-123"),
		}
		saga.orderID = "order-123"

		TestTimeout(t, saga).
			ThenOnTimeout(TestCancelOrder{OrderID: "order-123", Reason: "Timeout"})
	})
}

// =============================================================================
// Error Handling Tests
// =============================================================================

type testErrorSaga struct {
	id string
}

func (s *testErrorSaga) Name() string { return "TestErrorSaga" }
func (s *testErrorSaga) HandleEvent(ctx context.Context, event mink.StoredEvent) ([]mink.Command, error) {
	return nil, errors.New("saga error")
}
func (s *testErrorSaga) IsComplete() bool   { return false }
func (s *testErrorSaga) State() interface{} { return nil }

func TestSagaTestFixture_ThenError(t *testing.T) {
	t.Run("passes when error occurs", func(t *testing.T) {
		saga := &testErrorSaga{id: "saga-123"}

		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "SomeEvent"}).
			ThenError(errors.New("saga error"))
	})
}

// =============================================================================
// Integration Tests
// =============================================================================

func TestSagaFixture_CompleteFlow(t *testing.T) {
	t.Run("order fulfillment happy path", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		// Step 1: Order placed
		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
			ThenCommands(TestRequestPayment{OrderID: "order-123", Amount: 100.0}).
			ThenState(SagaStateInProgress).
			ThenNotCompleted()

		// Step 2: Payment received
		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestPaymentReceived"}).
			ThenCommands(TestReserveInventory{OrderID: "order-123"}).
			ThenNotCompleted()

		// Step 3: Inventory reserved
		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestInventoryReserved"}).
			ThenCommands(TestScheduleShipment{OrderID: "order-123"}).
			ThenNotCompleted()

		// Step 4: Shipment scheduled
		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestShipmentScheduled"}).
			ThenNoCommands().
			ThenCompleted()
	})

	t.Run("payment failure path", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		// Step 1: Order placed
		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
			ThenCommands(TestRequestPayment{OrderID: "order-123", Amount: 100.0})

		// Step 2: Payment failed
		TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "TestPaymentFailed"}).
			ThenCommands(TestCancelOrder{OrderID: "order-123", Reason: "Payment failed"}).
			ThenState(SagaStateFailed)
	})
}

// =============================================================================
// SagaState Tests
// =============================================================================

func TestSagaState_Constants(t *testing.T) {
	t.Run("state constants have expected values", func(t *testing.T) {
		assert.Equal(t, SagaState("NotStarted"), SagaStateNotStarted)
		assert.Equal(t, SagaState("InProgress"), SagaStateInProgress)
		assert.Equal(t, SagaState("Completed"), SagaStateCompleted)
		assert.Equal(t, SagaState("Failed"), SagaStateFailed)
		assert.Equal(t, SagaState("Compensating"), SagaStateCompensating)
	})
}

// =============================================================================
// Edge Cases
// =============================================================================

func TestSagaTestFixture_EmptyEvents(t *testing.T) {
	t.Run("handles no events", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		fixture := TestSaga(t, saga).
			GivenEvents() // No events

		require.Empty(t, fixture.commands)
	})
}

func TestSagaTestFixture_UnknownEvent(t *testing.T) {
	t.Run("handles unknown event type", func(t *testing.T) {
		saga := newTestOrderFulfillmentSaga("saga-123")

		fixture := TestSaga(t, saga).
			GivenEvents(mink.StoredEvent{Type: "UnknownEvent"})

		assert.Empty(t, fixture.commands)
	})
}

// =============================================================================
// Failure Path Tests
// =============================================================================

func TestSagaTestFixture_ThenCommands_FailureCases(t *testing.T) {
	t.Run("fails when saga returned error", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newErrorSaga()
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "ErrorEvent"}).
				ThenCommands()
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when command count mismatch", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
				ThenCommands() // expecting 0 commands but saga produces 1
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when command mismatch", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
				ThenCommands(TestRequestPayment{OrderID: "wrong-id", Amount: 999.0})
		})
		assert.True(t, mt.failed)
	})
}

func TestSagaTestFixture_ThenNoCommands_FailureCases(t *testing.T) {
	t.Run("fails when saga returned error", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newErrorSaga()
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "ErrorEvent"}).
				ThenNoCommands()
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when commands were produced", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
				ThenNoCommands()
		})
		assert.True(t, mt.failed)
	})
}

func TestSagaTestFixture_ThenCompleted_FailureCases(t *testing.T) {
	t.Run("fails when saga not completed", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
				ThenCompleted()
		})
		assert.True(t, mt.failed)
	})
}

func TestSagaTestFixture_ThenNotCompleted_FailureCases(t *testing.T) {
	t.Run("fails when saga is completed", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			// Simulate completed saga
			saga.state = SagaStateCompleted
			TestSaga(m, saga).
				ThenNotCompleted()
		})
		assert.True(t, mt.failed)
	})
}

func TestSagaTestFixture_ThenState_FailureCases(t *testing.T) {
	t.Run("fails when state doesn't match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
				ThenState(SagaStateCompleted)
		})
		assert.True(t, mt.failed)
	})
}

func TestSagaTestFixture_ThenError_FailureCases(t *testing.T) {
	t.Run("fails when no error returned", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
				ThenError(errors.New("expected error"))
		})
		assert.True(t, mt.failed)
	})

	t.Run("fails when wrong error", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newErrorSaga()
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "ErrorEvent"}).
				ThenError(errors.New("wrong error"))
		})
		assert.True(t, mt.failed)
	})
}

func TestSagaTestFixture_ThenCommandCount_FailureCases(t *testing.T) {
	t.Run("fails when count mismatch", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
				ThenCommandCount(5)
		})
		assert.True(t, mt.failed)
	})
}

func TestSagaTestFixture_ThenFirstCommand_FailureCases(t *testing.T) {
	t.Run("fails when no commands", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				ThenFirstCommand(TestRequestPayment{})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when first command doesn't match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
				ThenFirstCommand(TestRequestPayment{OrderID: "wrong-id", Amount: 999.0})
		})
		assert.True(t, mt.failed)
	})
}

func TestSagaTestFixture_ThenLastCommand_FailureCases(t *testing.T) {
	t.Run("fails when no commands", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				ThenLastCommand(TestRequestPayment{})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when last command doesn't match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
				ThenLastCommand(TestRequestPayment{OrderID: "wrong-id", Amount: 999.0})
		})
		assert.True(t, mt.failed)
	})
}

func TestSagaTestFixture_ThenContainsCommand_FailureCases(t *testing.T) {
	t.Run("fails when command not found", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestSaga(m, saga).
				GivenEvents(mink.StoredEvent{Type: "TestOrderPlaced"}).
				ThenContainsCommand(TestCancelOrder{OrderID: "order-123"})
		})
		assert.True(t, mt.failed)
	})
}

func TestCompensationFixture_ThenCompensates_FailureCases(t *testing.T) {
	t.Run("fails when no compensation commands", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestCompensation(m, saga).
				ThenCompensates(TestRefundPayment{OrderID: "order-123"})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when compensation command count mismatch", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			// Set up saga with compensation commands
			saga.compensationCommands = []mink.Command{
				TestRefundPayment{OrderID: "order-123", PaymentID: "payment-123"},
			}
			TestCompensation(m, saga).
				GivenFailureAfter(mink.StoredEvent{Type: "TestInventoryReservationFailed"}).
				ThenCompensates(
					TestRefundPayment{OrderID: "order-123", PaymentID: "payment-123"},
					TestReleaseInventory{OrderID: "order-123"},
				)
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})
}

func TestTimeoutFixture_ThenOnTimeout_FailureCases(t *testing.T) {
	t.Run("fails when saga doesn't support timeout", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			saga := newTestOrderFulfillmentSaga("saga-123")
			TestTimeout(m, saga).
				ThenOnTimeout(TestCancelOrder{})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})
}

// errorSaga is a saga that returns errors for testing
type errorSaga struct{}

func newErrorSaga() *errorSaga { return &errorSaga{} }

func (s *errorSaga) Name() string { return "ErrorSaga" }
func (s *errorSaga) HandleEvent(ctx context.Context, event mink.StoredEvent) ([]mink.Command, error) {
	return nil, errors.New("saga error")
}
func (s *errorSaga) IsComplete() bool   { return false }
func (s *errorSaga) State() interface{} { return nil }

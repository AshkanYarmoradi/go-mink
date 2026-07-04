package mink_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mink "go-mink.dev"
	"go-mink.dev/adapters/postgres"
	"go-mink.dev/outbox/webhook"
)

type sagaOrderPlaced struct {
	OrderID string `json:"orderId"`
}
type sagaPaymentTaken struct {
	OrderID string `json:"orderId"`
}

type takePaymentCmd struct {
	mink.CommandBase
	OrderID string
}

func (c *takePaymentCmd) CommandType() string { return "TakePayment" }
func (c *takePaymentCmd) Validate() error     { return nil }

type releaseOrderCmd struct {
	mink.CommandBase
	OrderID string
}

func (c *releaseOrderCmd) CommandType() string { return "ReleaseOrder" }
func (c *releaseOrderCmd) Validate() error     { return nil }

// e2eOrderSaga: OrderPlaced -> TakePayment command; PaymentTaken completes it; failure compensates.
type e2eOrderSaga struct {
	mink.SagaBase
	data     map[string]interface{}
	complete bool
}

func newE2EOrderSaga(id string) mink.Saga {
	return &e2eOrderSaga{SagaBase: mink.NewSagaBase(id, "OrderSaga")}
}
func (s *e2eOrderSaga) HandledEvents() []string {
	return []string{"sagaOrderPlaced", "sagaPaymentTaken"}
}
func (s *e2eOrderSaga) HandleEvent(_ context.Context, e mink.StoredEvent) ([]mink.Command, error) {
	switch e.Type {
	case "sagaOrderPlaced":
		return []mink.Command{&takePaymentCmd{OrderID: e.StreamID}}, nil
	case "sagaPaymentTaken":
		s.complete = true
		return nil, nil
	}
	return nil, nil
}
func (s *e2eOrderSaga) Compensate(_ context.Context, _ int, _ error) ([]mink.Command, error) {
	return []mink.Command{&releaseOrderCmd{OrderID: s.CorrelationID()}}, nil
}
func (s *e2eOrderSaga) IsComplete() bool { return s.complete }
func (s *e2eOrderSaga) Data() map[string]interface{} {
	if s.data == nil {
		s.data = map[string]interface{}{}
	}
	return s.data
}
func (s *e2eOrderSaga) SetData(d map[string]interface{}) { s.data = d }

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func newSagaManager(t *testing.T, p *e2ePG, bus *mink.CommandBus) *mink.SagaManager {
	t.Helper()
	sagaStore := postgres.NewSagaStoreFromAdapter(p.Adapter)
	require.NoError(t, sagaStore.Initialize(p.Ctx))
	mgr := mink.NewSagaManager(p.Store,
		mink.WithSagaStore(sagaStore),
		mink.WithCommandBus(bus),
		mink.WithSagaRetryAttempts(1),
		mink.WithSagaRetryDelay(time.Millisecond),
	)
	mgr.RegisterSimple("OrderSaga", newE2EOrderSaga, "sagaOrderPlaced")
	return mgr
}

// TestE2E_SagaOutbox_HappyPath: a saga reacts to a PG-stored event, its command handler appends a
// follow-up event through the outbox, the OutboxProcessor delivers it to a real webhook, and the
// saga reaches completion.
func TestE2E_SagaOutbox_HappyPath(t *testing.T) {
	p := newE2EPG(t, sagaOrderPlaced{}, sagaPaymentTaken{})

	var delivered atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Outbox-event-type") == "sagaPaymentTaken" {
			delivered.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	outboxStore := postgres.NewOutboxStoreFromAdapter(p.Adapter)
	require.NoError(t, outboxStore.Initialize(p.Ctx))
	esOut := mink.NewEventStoreWithOutbox(p.Store, outboxStore,
		[]mink.OutboxRoute{{EventTypes: []string{"sagaPaymentTaken"}, Destination: "webhook:" + server.URL}})
	proc := mink.NewOutboxProcessor(outboxStore, mink.WithPublisher(webhook.New()), mink.WithPollInterval(50*time.Millisecond))
	require.NoError(t, proc.Start(p.Ctx))
	defer func() { _ = proc.Stop(context.Background()) }()

	bus := mink.NewCommandBus()
	bus.RegisterFunc("TakePayment", func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		c := cmd.(*takePaymentCmd)
		if err := esOut.Append(ctx, c.OrderID, []interface{}{sagaPaymentTaken{OrderID: c.OrderID}}); err != nil {
			return mink.NewErrorResult(err), err
		}
		return mink.NewSuccessResult(c.OrderID, 0), nil
	})

	mgr := newSagaManager(t, p, bus)
	mgr.StartAsync(p.Ctx)
	defer mgr.Stop()

	require.NoError(t, p.Store.Append(p.Ctx, "order-h1", []interface{}{sagaOrderPlaced{OrderID: "order-h1"}}))

	eventually(t, 20*time.Second, func() bool {
		st, err := mgr.FindSagaByCorrelationID(p.Ctx, "order-h1")
		return err == nil && st.Status == mink.SagaStatusCompleted
	})
	eventually(t, 10*time.Second, func() bool { return delivered.Load() >= 1 })
}

// TestE2E_SagaOutbox_Compensation: a failing command drives the saga through compensation.
func TestE2E_SagaOutbox_Compensation(t *testing.T) {
	p := newE2EPG(t, sagaOrderPlaced{})

	var released atomic.Int32
	bus := mink.NewCommandBus()
	bus.RegisterFunc("TakePayment", func(_ context.Context, _ mink.Command) (mink.CommandResult, error) {
		err := errors.New("payment gateway down") // business failure -> compensate
		return mink.NewErrorResult(err), err
	})
	bus.RegisterFunc("ReleaseOrder", func(_ context.Context, _ mink.Command) (mink.CommandResult, error) {
		released.Add(1)
		return mink.NewSuccessResult("", 0), nil
	})

	mgr := newSagaManager(t, p, bus)
	ev := mink.StoredEvent{ID: "c-evt-1", StreamID: "order-c1", Type: "sagaOrderPlaced",
		Data: mustJSON(t, sagaOrderPlaced{OrderID: "order-c1"}), GlobalPosition: 1, Version: 1}
	require.NoError(t, mgr.ProcessEvent(p.Ctx, ev))

	st, err := mgr.FindSagaByCorrelationID(p.Ctx, "order-c1")
	require.NoError(t, err)
	assert.Equal(t, mink.SagaStatusCompensated, st.Status, "a failed command compensates the saga")
	assert.Equal(t, int32(1), released.Load(), "the compensating command was dispatched")
}

// TestE2E_SagaOutbox_ShutdownDoesNotCompensate: a context-cancellation during dispatch (graceful
// shutdown) leaves the saga for restart rather than compensating on a dead context.
func TestE2E_SagaOutbox_ShutdownDoesNotCompensate(t *testing.T) {
	p := newE2EPG(t, sagaOrderPlaced{})

	var released atomic.Int32
	bus := mink.NewCommandBus()
	bus.RegisterFunc("TakePayment", func(_ context.Context, _ mink.Command) (mink.CommandResult, error) {
		return mink.NewErrorResult(context.Canceled), context.Canceled // shutdown, not a business failure
	})
	bus.RegisterFunc("ReleaseOrder", func(_ context.Context, _ mink.Command) (mink.CommandResult, error) {
		released.Add(1)
		return mink.NewSuccessResult("", 0), nil
	})

	mgr := newSagaManager(t, p, bus)
	ev := mink.StoredEvent{ID: "s-evt-1", StreamID: "order-s1", Type: "sagaOrderPlaced",
		Data: mustJSON(t, sagaOrderPlaced{OrderID: "order-s1"}), GlobalPosition: 1, Version: 1}
	_ = mgr.ProcessEvent(p.Ctx, ev) // may surface the shutdown error; not asserted

	assert.Zero(t, released.Load(), "a context cancellation must not drive compensation")
	if st, err := mgr.FindSagaByCorrelationID(p.Ctx, "order-s1"); err == nil {
		assert.NotEqual(t, mink.SagaStatusCompensated, st.Status)
		assert.NotEqual(t, mink.SagaStatusCompensationFailed, st.Status)
	}
}

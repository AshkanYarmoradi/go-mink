package mink_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mink "go-mink.dev"
	"go-mink.dev/adapters/postgres"
)

// createOrderCmd is dispatched through the command bus; its handler appends an e2eOrderPlaced
// event to the PostgreSQL store. It implements IdempotentCommand so the idempotency middleware
// dedups on Key.
type createOrderCmd struct {
	mink.CommandBase
	OrderID string
	Amount  int
	Key     string
}

func (c *createOrderCmd) CommandType() string { return "CreateOrder" }
func (c *createOrderCmd) Validate() error {
	if c.Amount <= 0 {
		return mink.NewValidationError("CreateOrder", "Amount", "must be positive")
	}
	return nil
}
func (c *createOrderCmd) IdempotencyKey() string { return c.Key }

// e2eAsyncProjection records the events it applies (a simple in-memory read model). failOn maps
// an order id to a forced Apply error, to exercise the poison-event path.
type e2eAsyncProjection struct {
	mink.AsyncProjectionBase
	mu      sync.Mutex
	applied []string // order ids applied, in order
	failOn  map[string]bool
}

func newE2EAsyncProjection(name string, failOn map[string]bool) *e2eAsyncProjection {
	return &e2eAsyncProjection{
		AsyncProjectionBase: mink.NewAsyncProjectionBase(name, "e2eOrderPlaced"),
		failOn:              failOn,
	}
}

func (p *e2eAsyncProjection) Apply(_ context.Context, ev mink.StoredEvent) error {
	var o e2eOrderPlaced
	if err := json.Unmarshal(ev.Data, &o); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failOn[o.OrderID] {
		return fmt.Errorf("poison order %s", o.OrderID)
	}
	p.applied = append(p.applied, o.OrderID)
	return nil
}

func (p *e2eAsyncProjection) appliedIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.applied...)
}

// newCommandBus wires the full middleware pipeline (recovery, correlation, validation,
// idempotency backed by a postgres store) and a handler that appends to the store.
func newCommandBus(t *testing.T, p *e2ePG) *mink.CommandBus {
	t.Helper()
	idem := postgres.NewIdempotencyStoreFromAdapter(p.Adapter)
	require.NoError(t, idem.Initialize(p.Ctx))

	bus := mink.NewCommandBus()
	bus.Use(
		mink.RecoveryMiddleware(),
		mink.CorrelationIDMiddleware(nil),
		mink.ValidationMiddleware(),
		mink.IdempotencyMiddleware(mink.DefaultIdempotencyConfig(idem)),
	)
	bus.RegisterFunc("CreateOrder", func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
		c := cmd.(*createOrderCmd)
		if err := p.Store.Append(ctx, "order-"+c.OrderID,
			[]interface{}{e2eOrderPlaced{OrderID: c.OrderID, Amount: c.Amount}}); err != nil {
			return mink.NewErrorResult(err), err
		}
		return mink.NewSuccessResult(c.OrderID, 1), nil
	})
	return bus
}

// TestE2E_ProjectionPipeline_CommandToReadModel: bus -> PG store -> async projection -> read
// model, asserting the read model is updated and the checkpoint advances.
func TestE2E_ProjectionPipeline_CommandToReadModel(t *testing.T) {
	p := newE2EPG(t, e2eOrderPlaced{})
	bus := newCommandBus(t, p)
	defer func() { _ = bus.Close() }()

	proj := newE2EAsyncProjection("order-count", nil)
	engine := mink.NewProjectionEngine(p.Store, mink.WithCheckpointStore(p.Adapter))
	opts := mink.DefaultAsyncOptions()
	opts.PollInterval = 25 * time.Millisecond
	require.NoError(t, engine.RegisterAsync(proj, opts))
	require.NoError(t, engine.Start(p.Ctx))
	defer func() { _ = engine.Stop(context.Background()) }()

	for i := 0; i < 3; i++ {
		res, err := bus.Dispatch(p.Ctx, &createOrderCmd{OrderID: fmt.Sprintf("c%d", i), Amount: 10, Key: fmt.Sprintf("k%d", i)})
		require.NoError(t, err)
		require.True(t, res.Success)
	}

	eventually(t, 15*time.Second, func() bool { return len(proj.appliedIDs()) == 3 })
	assert.ElementsMatch(t, []string{"c0", "c1", "c2"}, proj.appliedIDs())

	pos, err := p.Adapter.GetCheckpoint(p.Ctx, proj.Name())
	require.NoError(t, err)
	assert.Positive(t, pos, "checkpoint advanced past the processed events")
}

// TestE2E_ProjectionPipeline_Idempotent: dispatching the same command twice appends once.
func TestE2E_ProjectionPipeline_Idempotent(t *testing.T) {
	p := newE2EPG(t, e2eOrderPlaced{})
	bus := newCommandBus(t, p)
	defer func() { _ = bus.Close() }()

	cmd := &createOrderCmd{OrderID: "dup", Amount: 5, Key: "same-key"}
	r1, err := bus.Dispatch(p.Ctx, cmd)
	require.NoError(t, err)
	require.True(t, r1.Success)
	_, err = bus.Dispatch(p.Ctx, cmd) // same idempotency key
	require.NoError(t, err)

	events, err := p.Store.Load(p.Ctx, "order-dup")
	require.NoError(t, err)
	assert.Len(t, events, 1, "idempotent command must append exactly once")
}

// TestE2E_ProjectionPipeline_CheckpointRecovery: a restarted async worker resumes from its
// persisted checkpoint rather than replaying from position 0.
func TestE2E_ProjectionPipeline_CheckpointRecovery(t *testing.T) {
	p := newE2EPG(t, e2eOrderPlaced{})

	// First batch of events.
	for i := 0; i < 3; i++ {
		require.NoError(t, p.Store.Append(p.Ctx, fmt.Sprintf("order-r1-%d", i),
			[]interface{}{e2eOrderPlaced{OrderID: fmt.Sprintf("r1-%d", i), Amount: 1}}))
	}

	proj1 := newE2EAsyncProjection("recover-proj", nil)
	engine1 := mink.NewProjectionEngine(p.Store, mink.WithCheckpointStore(p.Adapter))
	opts := mink.DefaultAsyncOptions()
	opts.PollInterval = 25 * time.Millisecond
	require.NoError(t, engine1.RegisterAsync(proj1, opts))
	require.NoError(t, engine1.Start(p.Ctx))
	eventually(t, 15*time.Second, func() bool { return len(proj1.appliedIDs()) == 3 })
	require.NoError(t, engine1.Stop(context.Background())) // "crash"
	checkpointAfter1, err := p.Adapter.GetCheckpoint(p.Ctx, "recover-proj")
	require.NoError(t, err)
	require.Positive(t, checkpointAfter1)

	// Second batch appended while the projection is down.
	for i := 0; i < 2; i++ {
		require.NoError(t, p.Store.Append(p.Ctx, fmt.Sprintf("order-r2-%d", i),
			[]interface{}{e2eOrderPlaced{OrderID: fmt.Sprintf("r2-%d", i), Amount: 1}}))
	}

	// Fresh engine + fresh projection instance, same name -> resumes from the checkpoint.
	proj2 := newE2EAsyncProjection("recover-proj", nil)
	engine2 := mink.NewProjectionEngine(p.Store, mink.WithCheckpointStore(p.Adapter))
	require.NoError(t, engine2.RegisterAsync(proj2, opts))
	require.NoError(t, engine2.Start(p.Ctx))
	defer func() { _ = engine2.Stop(context.Background()) }()

	eventually(t, 15*time.Second, func() bool { return len(proj2.appliedIDs()) == 2 })
	assert.ElementsMatch(t, []string{"r2-0", "r2-1"}, proj2.appliedIDs(),
		"resumed worker applies only events after the checkpoint, not the full history")
}

// TestE2E_ProjectionPipeline_PoisonEvent: an event whose Apply always fails is delivered to
// OnPoisonEvent (which skips it) and the worker advances past it to later events.
func TestE2E_ProjectionPipeline_PoisonEvent(t *testing.T) {
	p := newE2EPG(t, e2eOrderPlaced{})

	orderIDs := []string{"ok-1", "poison", "ok-2"}
	for _, id := range orderIDs {
		require.NoError(t, p.Store.Append(p.Ctx, "order-"+id,
			[]interface{}{e2eOrderPlaced{OrderID: id, Amount: 1}}))
	}

	proj := newE2EAsyncProjection("poison-proj", map[string]bool{"poison": true})
	var poisoned []string
	var pmu sync.Mutex

	engine := mink.NewProjectionEngine(p.Store, mink.WithCheckpointStore(p.Adapter))
	opts := mink.DefaultAsyncOptions()
	opts.PollInterval = 25 * time.Millisecond
	opts.MaxRetries = 1
	// BatchSize 1 isolates each event in its own batch, so ApplyBatch's opaque failure maps to
	// exactly the poison event (a larger batch would skip the whole batch tail on the failure).
	opts.BatchSize = 1
	opts.OnPoisonEvent = func(_ context.Context, ev mink.StoredEvent, _ error) error {
		var o e2eOrderPlaced
		_ = json.Unmarshal(ev.Data, &o)
		pmu.Lock()
		poisoned = append(poisoned, o.OrderID)
		pmu.Unlock()
		return nil // skip the poison event and continue
	}
	require.NoError(t, engine.RegisterAsync(proj, opts))
	require.NoError(t, engine.Start(p.Ctx))
	defer func() { _ = engine.Stop(context.Background()) }()

	// Both non-poison events are eventually applied (the poison one, isolated in its own batch,
	// is skipped after retries); the worker advances past it rather than stalling.
	eventually(t, 25*time.Second, func() bool {
		ids := proj.appliedIDs()
		return contains(ids, "ok-1") && contains(ids, "ok-2")
	})
	assert.ElementsMatch(t, []string{"ok-1", "ok-2"}, proj.appliedIDs())

	pmu.Lock()
	defer pmu.Unlock()
	assert.Contains(t, poisoned, "poison", "the poison event was delivered to OnPoisonEvent")
}

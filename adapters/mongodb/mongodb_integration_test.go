package mongodb

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mink "go-mink.dev"
	"go-mink.dev/adapters"
)

func newIntegrationAdapter(t *testing.T, envVar string, opts ...Option) (*MongoAdapter, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping MongoDB integration test in short mode")
	}

	uri := os.Getenv(envVar)
	if uri == "" {
		t.Skipf("%s is not set", envVar)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	database := "mink_test_" + uuid.NewString()
	all := append([]Option{WithDatabase(database)}, opts...)
	adapter, err := NewAdapter(uri, all...)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = adapter.Database().Drop(context.Background())
		_ = adapter.Close()
	})

	require.NoError(t, adapter.Initialize(ctx))
	return adapter, ctx
}

func waitForSubscriptionEvent(t *testing.T, ch <-chan adapters.StoredEvent) adapters.StoredEvent {
	t.Helper()
	select {
	case event, ok := <-ch:
		require.True(t, ok, "subscription channel closed before receiving an event")
		return event
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for subscription event")
		return adapters.StoredEvent{}
	}
}

func TestMongoAdapterIntegration_EventStoreLifecycle(t *testing.T) {
	adapter, ctx := newIntegrationAdapter(t, "TEST_MONGODB_URL")

	events := []adapters.EventRecord{
		{
			Type: "OrderCreated",
			Data: []byte(`{"orderId":"order-1"}`),
			Metadata: adapters.Metadata{
				CorrelationID: "corr-1",
				Custom:        map[string]string{"source": "integration"},
			},
		},
		{
			Type: "OrderShipped",
			Data: []byte(`{"orderId":"order-1"}`),
		},
	}

	stored, err := adapter.Append(ctx, "order-1", events, NoStream)
	require.NoError(t, err)
	require.Len(t, stored, 2)
	assert.Equal(t, int64(1), stored[0].Version)
	assert.Equal(t, int64(2), stored[1].Version)
	assert.Equal(t, uint64(1), stored[0].GlobalPosition)

	loaded, err := adapter.Load(ctx, "order-1", 0)
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	assert.Equal(t, stored[0].ID, loaded[0].ID)
	assert.Equal(t, events[0].Data, loaded[0].Data)
	assert.Equal(t, events[0].Metadata, loaded[0].Metadata)

	info, err := adapter.GetStreamInfo(ctx, "order-1")
	require.NoError(t, err)
	assert.Equal(t, "order", info.Category)
	assert.Equal(t, int64(2), info.Version)

	_, err = adapter.Append(ctx, "order-1", []adapters.EventRecord{{Type: "OrderCancelled", Data: []byte(`{}`)}}, NoStream)
	require.Error(t, err)
	assert.True(t, errors.Is(err, adapters.ErrConcurrencyConflict))
}

func TestMongoAdapterIntegration_SnapshotsAndCheckpoints(t *testing.T) {
	adapter, ctx := newIntegrationAdapter(t, "TEST_MONGODB_URL")

	require.NoError(t, adapter.SaveSnapshot(ctx, "order-1", 3, []byte(`{"status":"paid"}`)))

	snapshot, err := adapter.LoadSnapshot(ctx, "order-1")
	require.NoError(t, err)
	assert.Equal(t, "order-1", snapshot.StreamID)
	assert.Equal(t, int64(3), snapshot.Version)
	assert.JSONEq(t, `{"status":"paid"}`, string(snapshot.Data))

	require.NoError(t, adapter.SetCheckpoint(ctx, "orders", 12))
	position, err := adapter.GetCheckpoint(ctx, "orders")
	require.NoError(t, err)
	assert.Equal(t, uint64(12), position)
}

func TestMongoRepositoryIntegration_RunTransactionCommitRollback(t *testing.T) {
	adapter, ctx := newIntegrationAdapter(t, "TEST_MONGODB_URL")
	repo, err := NewMongoRepositoryFromAdapter[taggedReadModel](adapter, WithReadModelCollection("tx_models"))
	require.NoError(t, err)

	err = repo.RunTransaction(ctx, func(txCtx context.Context, tx *TxRepository[taggedReadModel]) error {
		return tx.Insert(txCtx, &taggedReadModel{
			ID:         "commit-1",
			CustomerID: "customer-1",
			Email:      "commit@example.com",
			Total:      12.5,
		})
	})
	require.NoError(t, err)

	committed, err := repo.Get(ctx, "commit-1")
	require.NoError(t, err)
	assert.Equal(t, "customer-1", committed.CustomerID)

	rollbackErr := errors.New("rollback requested")
	err = repo.RunTransaction(ctx, func(txCtx context.Context, tx *TxRepository[taggedReadModel]) error {
		require.NoError(t, tx.Insert(txCtx, &taggedReadModel{
			ID:         "rollback-1",
			CustomerID: "customer-2",
			Email:      "rollback@example.com",
		}))
		return rollbackErr
	})
	require.ErrorIs(t, err, rollbackErr)

	_, err = repo.Get(ctx, "rollback-1")
	require.ErrorIs(t, err, mink.ErrNotFound)
}

func TestMongoRepositoryIntegration_WithSessionContext(t *testing.T) {
	adapter, ctx := newIntegrationAdapter(t, "TEST_MONGODB_URL")
	repo, err := NewMongoRepositoryFromAdapter[taggedReadModel](adapter, WithReadModelCollection("manual_tx_models"))
	require.NoError(t, err)

	session, err := adapter.Client().StartSession()
	require.NoError(t, err)
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		tx := repo.WithSessionContext(sc)
		return nil, tx.Upsert(sc, &taggedReadModel{
			ID:         "manual-1",
			CustomerID: "customer-3",
			Email:      "manual@example.com",
			Total:      99,
		})
	})
	require.NoError(t, err)

	model, err := repo.Get(ctx, "manual-1")
	require.NoError(t, err)
	assert.Equal(t, 99.0, model.Total)
}

func TestMongoAdapterIntegration_ChangeStreamSubscriptions(t *testing.T) {
	adapter, ctx := newIntegrationAdapter(t, "TEST_MONGODB_URL", WithSubscriptionMode(SubscriptionModeChangeStream))
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	allCh, err := adapter.SubscribeAll(subCtx, 0)
	require.NoError(t, err)
	streamCh, err := adapter.SubscribeStream(subCtx, "order-1", 0)
	require.NoError(t, err)
	categoryCh, err := adapter.SubscribeCategory(subCtx, "order", 0)
	require.NoError(t, err)

	_, err = adapter.Append(ctx, "order-1", []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{"orderId":"order-1"}`)}}, NoStream)
	require.NoError(t, err)

	assert.Equal(t, "OrderCreated", waitForSubscriptionEvent(t, allCh).Type)
	assert.Equal(t, "order-1", waitForSubscriptionEvent(t, streamCh).StreamID)
	assert.Equal(t, "order-1", waitForSubscriptionEvent(t, categoryCh).StreamID)
}

func TestMongoAdapterIntegration_AutoSubscriptionFallbackForStandalone(t *testing.T) {
	adapter, ctx := newIntegrationAdapter(
		t,
		"TEST_MONGODB_STANDALONE_URL",
		WithTransactionMode(TransactionModeDisabled),
		WithSubscriptionMode(SubscriptionModeAuto),
	)
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch, err := adapter.SubscribeAll(subCtx, 0)
	require.NoError(t, err)

	_, err = adapter.Append(ctx, "order-1", []adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}}, NoStream)
	require.NoError(t, err)

	event := waitForSubscriptionEvent(t, ch)
	assert.Equal(t, "OrderCreated", event.Type)
}

func TestMongoAdapterIntegration_ChangeStreamModeRequiresSupport(t *testing.T) {
	adapter, ctx := newIntegrationAdapter(
		t,
		"TEST_MONGODB_STANDALONE_URL",
		WithTransactionMode(TransactionModeDisabled),
		WithSubscriptionMode(SubscriptionModeChangeStream),
	)

	_, err := adapter.SubscribeAll(ctx, 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start change stream subscription")
}

func TestMongoAdapterIntegration_OptionalStores(t *testing.T) {
	adapter, ctx := newIntegrationAdapter(t, "TEST_MONGODB_URL")
	now := time.Now().UTC()

	idempotency := NewIdempotencyStoreFromAdapter(adapter)
	record := &adapters.IdempotencyRecord{
		Key:         "cmd-1",
		CommandType: "CreateOrder",
		AggregateID: "order-1",
		Version:     1,
		Response:    []byte(`{"ok":true}`),
		Success:     true,
		ProcessedAt: now,
		ExpiresAt:   now.Add(time.Hour),
	}
	require.NoError(t, idempotency.Store(ctx, record))
	exists, err := idempotency.Exists(ctx, record.Key)
	require.NoError(t, err)
	assert.True(t, exists)
	loadedRecord, err := idempotency.Get(ctx, record.Key)
	require.NoError(t, err)
	assert.Equal(t, record.CommandType, loadedRecord.CommandType)
	require.NoError(t, idempotency.Delete(ctx, record.Key))

	outbox := NewOutboxStoreFromAdapter(adapter)
	message := &adapters.OutboxMessage{
		ID:          "msg-1",
		AggregateID: "order-1",
		EventType:   "OrderCreated",
		Destination: "webhook:https://example.com",
		Payload:     []byte(`{"orderId":"order-1"}`),
	}
	require.NoError(t, outbox.Schedule(ctx, []*adapters.OutboxMessage{message}))
	pending, err := outbox.FetchPending(ctx, 1)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, adapters.OutboxProcessing, pending[0].Status)
	require.NoError(t, outbox.MarkCompleted(ctx, []string{pending[0].ID}))

	sagas := NewSagaStoreFromAdapter(adapter)
	state := &adapters.SagaState{
		ID:            "saga-1",
		Type:          "order-fulfillment",
		CorrelationID: "order-1",
		Status:        adapters.SagaStatusRunning,
		StartedAt:     now,
		UpdatedAt:     now,
		Data:          map[string]interface{}{"order_id": "order-1"},
	}
	require.NoError(t, sagas.Save(ctx, state))
	loadedSaga, err := sagas.Load(ctx, state.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), loadedSaga.Version)
	byCorrelation, err := sagas.FindByCorrelationID(ctx, state.CorrelationID)
	require.NoError(t, err)
	assert.Equal(t, state.ID, byCorrelation.ID)
	byType, err := sagas.FindByType(ctx, state.Type, adapters.SagaStatusRunning)
	require.NoError(t, err)
	require.Len(t, byType, 1)
	counts, err := sagas.CountByStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), counts[adapters.SagaStatusRunning])
	require.NoError(t, sagas.Delete(ctx, state.ID))
}

func TestMongoRepositoryIntegration_CRUDQueryUpsertDelete(t *testing.T) {
	adapter, ctx := newIntegrationAdapter(t, "TEST_MONGODB_URL")
	repo, err := NewMongoRepositoryFromAdapter[taggedReadModel](adapter, WithReadModelCollection("crud_models"))
	require.NoError(t, err)

	require.NoError(t, repo.Insert(ctx, &taggedReadModel{
		ID:         "model-1",
		CustomerID: "customer-1",
		Email:      "model-1@example.com",
		Total:      10,
	}))
	exists, err := repo.Exists(ctx, "model-1")
	require.NoError(t, err)
	assert.True(t, exists)

	require.NoError(t, repo.Update(ctx, "model-1", func(model *taggedReadModel) {
		model.Total = 25
	}))
	updated, err := repo.Get(ctx, "model-1")
	require.NoError(t, err)
	assert.Equal(t, 25.0, updated.Total)

	require.NoError(t, repo.Upsert(ctx, &taggedReadModel{
		ID:         "model-2",
		CustomerID: "customer-1",
		Email:      "model-2@example.com",
		Total:      50,
	}))
	found, err := repo.Find(ctx, mink.Query{Filters: []mink.Filter{{Field: "CustomerID", Op: mink.FilterOpEq, Value: "customer-1"}}})
	require.NoError(t, err)
	assert.Len(t, found, 2)
	count, err := repo.Count(ctx, mink.Query{Filters: []mink.Filter{{Field: "CustomerID", Op: mink.FilterOpEq, Value: "customer-1"}}})
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
	one, err := repo.FindOne(ctx, mink.Query{Filters: []mink.Filter{{Field: "Email", Op: mink.FilterOpEq, Value: "model-2@example.com"}}})
	require.NoError(t, err)
	assert.Equal(t, "model-2", one.ID)

	deleted, err := repo.DeleteMany(ctx, mink.Query{Filters: []mink.Filter{{Field: "Total", Op: mink.FilterOpGte, Value: 50}}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	all, err := repo.GetAll(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.NoError(t, repo.Clear(ctx))
	all, err = repo.GetAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestMongoAdapterIntegration_OutboxAtomicityFallbackForStandalone(t *testing.T) {
	adapter, ctx := newIntegrationAdapter(t, "TEST_MONGODB_STANDALONE_URL", WithTransactionMode(TransactionModeDisabled))

	_, err := adapter.AppendWithOutbox(
		ctx,
		"order-1",
		[]adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{}`)}},
		NoStream,
		[]*adapters.OutboxMessage{{ID: "msg-1"}},
	)

	require.Error(t, err)
	assert.True(t, errors.Is(err, adapters.ErrOutboxAtomicityUnsupported))
}

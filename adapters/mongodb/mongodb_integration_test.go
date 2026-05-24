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

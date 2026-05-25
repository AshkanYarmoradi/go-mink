package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mink "go-mink.dev"
	"go-mink.dev/adapters"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type taggedReadModel struct {
	ID         string    `mink:"id,pk"`
	CustomerID string    `mink:"customer_id,index"`
	Email      string    `mink:"email,unique"`
	Total      float64   `mink:"total_amount,type=decimal,default=0"`
	ArchivedAt time.Time `mink:"archived_at,nullable"`
	Ignored    string    `mink:"-"`
}

func TestCollectionNameOptions(t *testing.T) {
	client, err := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017"))
	require.NoError(t, err)
	defer func() { _ = client.Disconnect(context.Background()) }()

	adapter, err := NewAdapterWithClient(
		client,
		WithDatabase("mink_custom"),
		WithCollectionPrefix("tenant_"),
		WithCollectionNames(CollectionNames{
			Events: "custom_events",
			Outbox: "custom_outbox",
		}),
		WithTransactionMode(TransactionModeDisabled),
		WithSubscriptionMode(SubscriptionModePolling),
	)
	require.NoError(t, err)

	names := adapter.CollectionNames()
	assert.Equal(t, "mink_custom", adapter.Database().Name())
	assert.Equal(t, "tenant_streams", names.Streams)
	assert.Equal(t, "custom_events", names.Events)
	assert.Equal(t, "tenant_snapshots", names.Snapshots)
	assert.Equal(t, "custom_outbox", names.Outbox)
	assert.Equal(t, TransactionModeDisabled, adapter.transactionMode)
	assert.Equal(t, SubscriptionModePolling, adapter.SubscriptionMode())
}

func TestInvalidCollectionNames(t *testing.T) {
	client, err := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017"))
	require.NoError(t, err)
	defer func() { _ = client.Disconnect(context.Background()) }()

	_, err = NewAdapterWithClient(client, WithCollectionNames(CollectionNames{Events: "bad$name"}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "events collection name contains invalid characters")
}

func TestTransactionModeResolution(t *testing.T) {
	tests := []struct {
		name       string
		mode       TransactionMode
		wantErr    bool
		wantActive bool
	}{
		{name: "disabled", mode: TransactionModeDisabled, wantErr: false, wantActive: false},
		{name: "invalid", mode: TransactionMode(99), wantErr: true, wantActive: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &MongoAdapter{transactionMode: tt.mode}

			err := adapter.resolveTransactionMode(context.Background())

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantActive, adapter.TransactionsActive())
		})
	}
}

func TestAppendWithOutboxRequiresActiveTransactions(t *testing.T) {
	adapter := &MongoAdapter{transactionsActive: false}

	_, err := adapter.AppendWithOutbox(
		context.Background(),
		"order-1",
		[]adapters.EventRecord{{Type: "OrderCreated", Data: []byte(`{"ok":true}`)}},
		NoStream,
		[]*adapters.OutboxMessage{{ID: "msg-1"}},
	)

	require.Error(t, err)
	assert.True(t, errors.Is(err, adapters.ErrOutboxAtomicityUnsupported))
}

func TestStoredEventConversionPreservesBinaryDataAndMetadata(t *testing.T) {
	now := time.Now().UTC()
	metadata := adapters.Metadata{
		CorrelationID: "corr-1",
		CausationID:   "cause-1",
		UserID:        "user-1",
		TenantID:      "tenant-1",
		Custom:        map[string]string{"source": "test"},
	}
	doc := eventDoc{
		ID:             "event-1",
		StreamID:       "order-1",
		Version:        2,
		Type:           "OrderUpdated",
		Data:           bson.Binary{Subtype: bson.TypeBinaryGeneric, Data: []byte{0x01, 0x02, 0x03}},
		Metadata:       metadataToDoc(metadata),
		GlobalPosition: 42,
		Timestamp:      now,
	}

	stored := storedEventFromDoc(doc)

	assert.Equal(t, doc.ID, stored.ID)
	assert.Equal(t, doc.StreamID, stored.StreamID)
	assert.Equal(t, doc.Type, stored.Type)
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, stored.Data)
	assert.Equal(t, metadata, stored.Metadata)
	assert.Equal(t, int64(2), stored.Version)
	assert.Equal(t, uint64(42), stored.GlobalPosition)
	assert.Equal(t, now, stored.Timestamp)
}

func TestTxRepositoryUsesSessionContext(t *testing.T) {
	client, err := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017"))
	require.NoError(t, err)
	defer func() { _ = client.Disconnect(context.Background()) }()

	session, err := client.StartSession()
	require.NoError(t, err)
	defer session.EndSession(context.Background())

	repo := &MongoRepository[taggedReadModel]{client: client}
	tx := repo.WithSessionContext(mongo.NewSessionContext(context.Background(), session))

	ctx := tx.contextFor(context.Background())

	assert.NotNil(t, mongo.SessionFromContext(ctx))
}

func TestRunTransactionRequiresCallback(t *testing.T) {
	repo := &MongoRepository[taggedReadModel]{}

	err := repo.RunTransaction(context.Background(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "transaction callback is nil")
}

func TestGenerateSchemaMongoScript(t *testing.T) {
	schema := GenerateSchema("demo", "mink_demo", CollectionNames{Events: "event_log", Outbox: "outbox_messages"})

	assert.Contains(t, schema, `use("mink_demo");`)
	assert.Contains(t, schema, `db.createCollection("event_log");`)
	assert.Contains(t, schema, `db.event_log.createIndex({ stream_id: 1, version: 1 }, { unique: true, name: "uidx_events_stream_version" });`)
	assert.Contains(t, schema, `db.outbox_messages.createIndex({ status: 1, scheduled_at: 1 }, { name: "idx_outbox_pending" });`)
	assert.Contains(t, schema, `db.counters.updateOne({ _id: "global_position" }`)
}

func TestReadModelTagParsing(t *testing.T) {
	info, err := buildReadModelInfo[taggedReadModel]("", "ID")
	require.NoError(t, err)

	assert.Equal(t, "tagged_read_model", info.collection)
	require.Len(t, info.fields, 5)
	assert.Equal(t, "id", info.fieldName("ID"))
	assert.Equal(t, "customer_id", info.fieldName("CustomerID"))
	assert.Equal(t, "total_amount", info.fieldName("Total"))
	assert.Equal(t, "", info.fieldName("Ignored"))

	fields := map[string]readModelField{}
	for _, field := range info.fields {
		fields[field.name] = field
	}
	assert.True(t, fields["id"].primaryKey)
	assert.True(t, fields["customer_id"].index)
	assert.True(t, fields["email"].index)
	assert.True(t, fields["email"].unique)
}

func TestReadModelDocumentMapping(t *testing.T) {
	info, err := buildReadModelInfo[taggedReadModel]("", "ID")
	require.NoError(t, err)
	repo := &MongoRepository[taggedReadModel]{model: info}

	model := &taggedReadModel{
		ID:         "summary-1",
		CustomerID: "customer-1",
		Email:      "test@example.com",
		Total:      42.5,
		Ignored:    "do-not-store",
	}

	doc, err := repo.structToDocument(model)
	require.NoError(t, err)

	assert.Equal(t, "summary-1", doc["_id"])
	assert.Equal(t, "summary-1", doc["id"])
	assert.Equal(t, "customer-1", doc["customer_id"])
	assert.Equal(t, "test@example.com", doc["email"])
	assert.Equal(t, 42.5, doc["total_amount"])
	assert.NotContains(t, doc, "ignored")

	roundTrip, err := repo.modelToStruct(doc)
	require.NoError(t, err)
	assert.Equal(t, model.ID, roundTrip.ID)
	assert.Equal(t, model.CustomerID, roundTrip.CustomerID)
	assert.Equal(t, model.Email, roundTrip.Email)
	assert.Equal(t, model.Total, roundTrip.Total)
}

func TestReadModelQueryFilterConversion(t *testing.T) {
	info, err := buildReadModelInfo[taggedReadModel]("", "ID")
	require.NoError(t, err)
	repo := &MongoRepository[taggedReadModel]{model: info}

	filter := repo.buildFilter([]mink.Filter{
		{Field: "CustomerID", Op: mink.FilterOpEq, Value: "customer-1"},
		{Field: "Total", Op: mink.FilterOpBetween, Value: []float64{10, 50}},
		{Field: "Ignored", Op: mink.FilterOpEq, Value: "skip"},
	})

	assert.Equal(t, bson.M{
		"$and": []bson.M{
			{"customer_id": "customer-1"},
			{"total_amount": bson.M{"$gte": 10.0, "$lte": 50.0}},
		},
	}, filter)
	assert.Equal(t, bson.M{"email": bson.M{"$regex": "^.*@example\\.com$"}}, mongoCondition("email", mink.FilterOpLike, "%@example.com"))
	assert.Equal(t, bson.M{"email": bson.M{"$in": []interface{}{"a", "b"}}}, mongoCondition("email", mink.FilterOpIn, []string{"a", "b"}))
}

func TestReadModelFindOptionsValidatePagination(t *testing.T) {
	info, err := buildReadModelInfo[taggedReadModel]("", "ID")
	require.NoError(t, err)
	repo := &MongoRepository[taggedReadModel]{model: info}

	_, err = repo.findOptions(mink.Query{Limit: -1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit must be non-negative")

	_, err = repo.findOptions(mink.Query{Offset: -1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offset must be non-negative")
}

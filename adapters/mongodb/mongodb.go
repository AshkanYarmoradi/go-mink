// Package mongodb provides a MongoDB implementation of the event store adapter.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	mink "go-mink.dev"
	"go-mink.dev/adapters"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

const (
	// AnyVersion skips version checking.
	AnyVersion = adapters.AnyVersion
	// NoStream requires the stream to not exist.
	NoStream = adapters.NoStream
	// StreamExists requires the stream to exist.
	StreamExists = adapters.StreamExists
)

var (
	// ErrAdapterClosed is returned when operations are attempted on a closed adapter.
	ErrAdapterClosed = adapters.ErrAdapterClosed
	// ErrEmptyStreamID is returned when an empty stream ID is provided.
	ErrEmptyStreamID = adapters.ErrEmptyStreamID
	// ErrNoEvents is returned when attempting to append zero events.
	ErrNoEvents = adapters.ErrNoEvents
	// ErrConcurrencyConflict is returned when optimistic concurrency check fails.
	ErrConcurrencyConflict = adapters.ErrConcurrencyConflict
	// ErrStreamNotFound is returned when a stream does not exist.
	ErrStreamNotFound = adapters.ErrStreamNotFound
	// ErrInvalidVersion is returned when an invalid version is specified.
	ErrInvalidVersion = adapters.ErrInvalidVersion
)

// TransactionMode controls how the adapter uses MongoDB transactions.
type TransactionMode int

const (
	// TransactionModeAuto uses transactions when available and falls back to standalone mode.
	TransactionModeAuto TransactionMode = iota
	// TransactionModeRequired requires transaction support during initialization.
	TransactionModeRequired
	// TransactionModeDisabled disables transactions for local standalone MongoDB usage.
	TransactionModeDisabled
)

// SubscriptionMode controls how MongoDB subscriptions are awakened.
type SubscriptionMode int

const (
	// SubscriptionModeAuto uses change streams when available and falls back to polling.
	SubscriptionModeAuto SubscriptionMode = iota
	// SubscriptionModePolling uses polling only.
	SubscriptionModePolling
	// SubscriptionModeChangeStream requires MongoDB change streams.
	SubscriptionModeChangeStream
)

// CollectionNames contains MongoDB collection names used by the adapter.
type CollectionNames struct {
	Streams      string
	Events       string
	Snapshots    string
	Checkpoints  string
	Migrations   string
	Idempotency  string
	Outbox       string
	Sagas        string
	Counters     string
	ResumeTokens string
}

// DefaultCollectionNames returns the default MongoDB collection names.
func DefaultCollectionNames() CollectionNames {
	return CollectionNames{
		Streams:      "streams",
		Events:       "events",
		Snapshots:    "snapshots",
		Checkpoints:  "checkpoints",
		Migrations:   "migrations",
		Idempotency:  "mink_idempotency",
		Outbox:       "mink_outbox",
		Sagas:        "mink_sagas",
		Counters:     "counters",
		ResumeTokens: "mink_resume_tokens",
	}
}

// MongoAdapter is a MongoDB implementation of EventStoreAdapter.
type MongoAdapter struct {
	client             *mongo.Client
	db                 *mongo.Database
	database           string
	collections        CollectionNames
	transactionMode    TransactionMode
	subscriptionMode   SubscriptionMode
	writeConcern       *writeconcern.WriteConcern
	readConcern        *readconcern.ReadConcern
	readPreference     *readpref.ReadPref
	transactionOptions []options.Lister[options.TransactionOptions]
	resumeTokenStore   *ResumeTokenStore
	transactionsActive bool
	ownsClient         bool
	closed             bool
}

// Option configures a MongoAdapter.
type Option func(*MongoAdapter)

// WithDatabase sets the MongoDB database name.
func WithDatabase(name string) Option {
	return func(a *MongoAdapter) {
		if name != "" {
			a.database = name
		}
	}
}

// WithCollectionPrefix prefixes all collection names.
func WithCollectionPrefix(prefix string) Option {
	return func(a *MongoAdapter) {
		if prefix == "" {
			return
		}
		a.collections = prefixCollections(a.collections, prefix)
	}
}

// WithCollectionNames overrides MongoDB collection names.
func WithCollectionNames(names CollectionNames) Option {
	return func(a *MongoAdapter) {
		a.collections = mergeCollectionNames(a.collections, names)
	}
}

// WithTransactionMode sets the transaction mode.
func WithTransactionMode(mode TransactionMode) Option {
	return func(a *MongoAdapter) {
		a.transactionMode = mode
	}
}

// WithSubscriptionMode sets the subscription mode.
func WithSubscriptionMode(mode SubscriptionMode) Option {
	return func(a *MongoAdapter) {
		a.subscriptionMode = mode
	}
}

// WithWriteConcern sets the MongoDB write concern used by adapter collections.
func WithWriteConcern(wc *writeconcern.WriteConcern) Option {
	return func(a *MongoAdapter) {
		a.writeConcern = wc
	}
}

// WithReadConcern sets the MongoDB read concern used by adapter collections.
func WithReadConcern(rc *readconcern.ReadConcern) Option {
	return func(a *MongoAdapter) {
		a.readConcern = rc
	}
}

// WithReadPreference sets the MongoDB read preference used by adapter collections.
func WithReadPreference(rp *readpref.ReadPref) Option {
	return func(a *MongoAdapter) {
		a.readPreference = rp
	}
}

// WithTransactionOptions sets the MongoDB transaction options used by adapter transactions.
func WithTransactionOptions(opts ...options.Lister[options.TransactionOptions]) Option {
	return func(a *MongoAdapter) {
		a.transactionOptions = append([]options.Lister[options.TransactionOptions]{}, opts...)
	}
}

// WithResumeTokenStore sets the persistent change-stream resume token store.
func WithResumeTokenStore(store *ResumeTokenStore) Option {
	return func(a *MongoAdapter) {
		a.resumeTokenStore = store
	}
}

// NewAdapter creates a new MongoDB event store adapter.
func NewAdapter(uri string, opts ...Option) (*MongoAdapter, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetAppName("go-mink"))
	if err != nil {
		return nil, fmt.Errorf("mink/mongodb: failed to create client: %w", err)
	}

	adapter, err := initAdapter(client, true, opts)
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return adapter, nil
}

// NewAdapterWithClient creates a new adapter with an existing MongoDB client.
func NewAdapterWithClient(client *mongo.Client, opts ...Option) (*MongoAdapter, error) {
	if client == nil {
		return nil, fmt.Errorf("mink/mongodb: client is nil")
	}
	return initAdapter(client, false, opts)
}

func initAdapter(client *mongo.Client, ownsClient bool, opts []Option) (*MongoAdapter, error) {
	adapter := &MongoAdapter{
		client:           client,
		database:         "mink",
		collections:      DefaultCollectionNames(),
		transactionMode:  TransactionModeAuto,
		subscriptionMode: SubscriptionModeAuto,
		ownsClient:       ownsClient,
	}

	for _, opt := range opts {
		opt(adapter)
	}

	if err := validateName(adapter.database, "database"); err != nil {
		return nil, err
	}
	if err := validateCollectionNames(adapter.collections); err != nil {
		return nil, err
	}

	adapter.db = client.Database(adapter.database, adapter.databaseOptions())
	if adapter.resumeTokenStore == nil {
		adapter.resumeTokenStore = NewResumeTokenStoreFromAdapter(adapter)
	}
	return adapter, nil
}

var (
	_ adapters.EventStoreAdapter            = (*MongoAdapter)(nil)
	_ adapters.SubscriptionAdapter          = (*MongoAdapter)(nil)
	_ adapters.SnapshotAdapter              = (*MongoAdapter)(nil)
	_ adapters.CheckpointAdapter            = (*MongoAdapter)(nil)
	_ adapters.HealthChecker                = (*MongoAdapter)(nil)
	_ adapters.Migrator                     = (*MongoAdapter)(nil)
	_ adapters.ProjectionTransactionAdapter = (*MongoAdapter)(nil)
	_ mink.CheckpointStore                  = (*MongoAdapter)(nil)
	_ adapters.OutboxAppender               = (*MongoAdapter)(nil)
)

type metadataDoc struct {
	CorrelationID string            `bson:"correlation_id,omitempty"`
	CausationID   string            `bson:"causation_id,omitempty"`
	UserID        string            `bson:"user_id,omitempty"`
	TenantID      string            `bson:"tenant_id,omitempty"`
	Custom        map[string]string `bson:"custom,omitempty"`
}

type streamDoc struct {
	ID         string    `bson:"_id"`
	Category   string    `bson:"category"`
	Version    int64     `bson:"version"`
	EventCount int64     `bson:"event_count"`
	CreatedAt  time.Time `bson:"created_at"`
	UpdatedAt  time.Time `bson:"updated_at"`
}

type eventDoc struct {
	ID             string      `bson:"_id"`
	StreamID       string      `bson:"stream_id"`
	Version        int64       `bson:"version"`
	Type           string      `bson:"type"`
	Data           bson.Binary `bson:"data"`
	Metadata       metadataDoc `bson:"metadata"`
	GlobalPosition int64       `bson:"global_position"`
	Timestamp      time.Time   `bson:"timestamp"`
}

type counterDoc struct {
	ID    string `bson:"_id"`
	Value int64  `bson:"value"`
}

func (a *MongoAdapter) collection(name string) *mongo.Collection {
	return a.db.Collection(name)
}

func (a *MongoAdapter) databaseOptions() *options.DatabaseOptionsBuilder {
	opts := options.Database()
	if a.writeConcern != nil {
		opts.SetWriteConcern(a.writeConcern)
	}
	if a.readConcern != nil {
		opts.SetReadConcern(a.readConcern)
	}
	if a.readPreference != nil {
		opts.SetReadPreference(a.readPreference)
	}
	return opts
}

func (a *MongoAdapter) streams() *mongo.Collection   { return a.collection(a.collections.Streams) }
func (a *MongoAdapter) events() *mongo.Collection    { return a.collection(a.collections.Events) }
func (a *MongoAdapter) snapshots() *mongo.Collection { return a.collection(a.collections.Snapshots) }
func (a *MongoAdapter) checkpoints() *mongo.Collection {
	return a.collection(a.collections.Checkpoints)
}
func (a *MongoAdapter) migrations() *mongo.Collection { return a.collection(a.collections.Migrations) }
func (a *MongoAdapter) idempotency() *mongo.Collection {
	return a.collection(a.collections.Idempotency)
}
func (a *MongoAdapter) outbox() *mongo.Collection   { return a.collection(a.collections.Outbox) }
func (a *MongoAdapter) sagas() *mongo.Collection    { return a.collection(a.collections.Sagas) }
func (a *MongoAdapter) counters() *mongo.Collection { return a.collection(a.collections.Counters) }
func (a *MongoAdapter) resumeTokens() *mongo.Collection {
	return a.collection(a.collections.ResumeTokens)
}

// Database returns the underlying MongoDB database handle.
func (a *MongoAdapter) Database() *mongo.Database {
	return a.db
}

// Client returns the underlying MongoDB client.
func (a *MongoAdapter) Client() *mongo.Client {
	return a.client
}

// CollectionNames returns the resolved collection names.
func (a *MongoAdapter) CollectionNames() CollectionNames {
	return a.collections
}

// TransactionsActive reports whether MongoDB transactions are being used.
func (a *MongoAdapter) TransactionsActive() bool {
	return a.transactionsActive
}

// SubscriptionMode returns the configured subscription mode.
func (a *MongoAdapter) SubscriptionMode() SubscriptionMode {
	return a.subscriptionMode
}

// WriteConcern returns the configured MongoDB write concern.
func (a *MongoAdapter) WriteConcern() *writeconcern.WriteConcern {
	return a.writeConcern
}

// ReadConcern returns the configured MongoDB read concern.
func (a *MongoAdapter) ReadConcern() *readconcern.ReadConcern {
	return a.readConcern
}

// ReadPreference returns the configured MongoDB read preference.
func (a *MongoAdapter) ReadPreference() *readpref.ReadPref {
	return a.readPreference
}

// Initialize creates indexes and detects transaction support.
func (a *MongoAdapter) Initialize(ctx context.Context) error {
	if a.closed {
		return ErrAdapterClosed
	}
	if err := a.Ping(ctx); err != nil {
		return err
	}
	if err := a.resolveTransactionMode(ctx); err != nil {
		return err
	}
	if err := a.createIndexes(ctx); err != nil {
		return err
	}
	if err := a.initializeCounter(ctx); err != nil {
		return err
	}
	return NewResumeTokenStoreFromAdapter(a).Initialize(ctx)
}

// Migrate runs MongoDB collection/index setup.
func (a *MongoAdapter) Migrate(ctx context.Context) error {
	return a.Initialize(ctx)
}

// MigrationVersion returns 1 when required collections are initialized.
func (a *MongoAdapter) MigrationVersion(ctx context.Context) (int, error) {
	count, err := a.events().Indexes().ListSpecifications(ctx)
	if err != nil {
		return 0, err
	}
	if len(count) == 0 {
		return 0, nil
	}
	return 1, nil
}

// Append stores events to the specified stream with optimistic concurrency control.
func (a *MongoAdapter) Append(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64) ([]adapters.StoredEvent, error) {
	if err := a.validateAppend(streamID, events); err != nil {
		return nil, err
	}
	if a.transactionsActive {
		return a.appendInTransaction(ctx, streamID, events, expectedVersion, nil)
	}
	return a.appendCore(ctx, streamID, events, expectedVersion, nil)
}

// AppendWithOutbox atomically appends events and schedules outbox messages when transactions are active.
func (a *MongoAdapter) AppendWithOutbox(ctx context.Context, streamID string, events []adapters.EventRecord, expectedVersion int64, outboxMessages []*adapters.OutboxMessage) ([]adapters.StoredEvent, error) {
	if err := a.validateAppend(streamID, events); err != nil {
		return nil, err
	}
	if !a.transactionsActive {
		return nil, adapters.ErrOutboxAtomicityUnsupported
	}
	return a.appendInTransaction(ctx, streamID, events, expectedVersion, outboxMessages)
}

func (a *MongoAdapter) validateAppend(streamID string, events []adapters.EventRecord) error {
	if a.closed {
		return ErrAdapterClosed
	}
	if streamID == "" {
		return ErrEmptyStreamID
	}
	if len(events) == 0 {
		return ErrNoEvents
	}
	return nil
}

func (a *MongoAdapter) appendInTransaction(ctx context.Context, streamID string, records []adapters.EventRecord, expectedVersion int64, outboxMessages []*adapters.OutboxMessage) ([]adapters.StoredEvent, error) {
	var stored []adapters.StoredEvent
	err := a.runTransaction(ctx, func(sc context.Context) error {
		var appendErr error
		stored, appendErr = a.appendCore(sc, streamID, records, expectedVersion, outboxMessages)
		return appendErr
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// RunProjectionTransaction runs projection updates and checkpoints in one MongoDB transaction.
func (a *MongoAdapter) RunProjectionTransaction(ctx context.Context, projectionName string, fn func(context.Context) error) error {
	if !a.transactionsActive {
		return adapters.ErrProjectionTransactionsUnsupported
	}
	if fn == nil {
		return fmt.Errorf("mink/mongodb: projection transaction callback is nil")
	}
	return a.runTransaction(ctx, func(sc context.Context) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("mink/mongodb: projection transaction %q panicked: %v", projectionName, recovered)
			}
		}()
		return fn(sc)
	})
}

func (a *MongoAdapter) runTransaction(ctx context.Context, fn func(context.Context) error) error {
	session, err := a.client.StartSession()
	if err != nil {
		return fmt.Errorf("mink/mongodb: failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		return nil, fn(sc)
	}, a.effectiveTransactionOptions()...)
	return err
}

func (a *MongoAdapter) effectiveTransactionOptions() []options.Lister[options.TransactionOptions] {
	return transactionOptionsWithConcerns(a.writeConcern, a.readConcern, a.readPreference, a.transactionOptions)
}

func (a *MongoAdapter) appendCore(ctx context.Context, streamID string, records []adapters.EventRecord, expectedVersion int64, outboxMessages []*adapters.OutboxMessage) ([]adapters.StoredEvent, error) {
	now := time.Now().UTC()
	oldVersion, err := a.advanceStreamVersion(ctx, streamID, int64(len(records)), expectedVersion, now)
	if err != nil {
		return nil, err
	}

	firstPosition, err := a.reservePositions(ctx, int64(len(records)))
	if err != nil {
		return nil, err
	}

	docs := make([]any, len(records))
	stored := make([]adapters.StoredEvent, len(records))
	for i, rec := range records {
		version := oldVersion + int64(i) + 1
		position := firstPosition + int64(i)
		id := uuid.NewString()

		doc := eventDoc{
			ID:             id,
			StreamID:       streamID,
			Version:        version,
			Type:           rec.Type,
			Data:           bson.Binary{Subtype: bson.TypeBinaryGeneric, Data: rec.Data},
			Metadata:       metadataToDoc(rec.Metadata),
			GlobalPosition: position,
			Timestamp:      now,
		}
		docs[i] = doc
		stored[i] = adapters.StoredEvent{
			ID:             id,
			StreamID:       streamID,
			Type:           rec.Type,
			Data:           rec.Data,
			Metadata:       rec.Metadata,
			Version:        version,
			GlobalPosition: uint64(position),
			Timestamp:      now,
		}
	}

	if _, err := a.events().InsertMany(ctx, docs); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, adapters.NewConcurrencyError(streamID, expectedVersion, oldVersion)
		}
		return nil, fmt.Errorf("mink/mongodb: failed to insert events: %w", err)
	}

	if len(outboxMessages) > 0 {
		if err := NewOutboxStoreFromAdapter(a).ScheduleInTx(ctx, ctx, outboxMessages); err != nil {
			return nil, err
		}
	}

	return stored, nil
}

func (a *MongoAdapter) advanceStreamVersion(ctx context.Context, streamID string, count, expectedVersion int64, now time.Time) (int64, error) {
	if expectedVersion < 0 && expectedVersion != adapters.AnyVersion && expectedVersion != adapters.StreamExists {
		return 0, adapters.ErrInvalidVersion
	}

	switch expectedVersion {
	case adapters.NoStream:
		doc := streamDoc{
			ID:         streamID,
			Category:   adapters.ExtractCategory(streamID),
			Version:    count,
			EventCount: count,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if _, err := a.streams().InsertOne(ctx, doc); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				actual := a.currentStreamVersion(ctx, streamID)
				return 0, adapters.NewConcurrencyError(streamID, expectedVersion, actual)
			}
			return 0, fmt.Errorf("mink/mongodb: failed to create stream: %w", err)
		}
		return 0, nil
	case adapters.AnyVersion:
		oldVersion, ok, err := a.incrementExistingStream(ctx, streamID, nil, count, now)
		if err != nil {
			return 0, err
		}
		if ok {
			return oldVersion, nil
		}
		doc := streamDoc{
			ID:         streamID,
			Category:   adapters.ExtractCategory(streamID),
			Version:    count,
			EventCount: count,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if _, err := a.streams().InsertOne(ctx, doc); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				oldVersion, _, retryErr := a.incrementExistingStream(ctx, streamID, nil, count, now)
				return oldVersion, retryErr
			}
			return 0, fmt.Errorf("mink/mongodb: failed to create stream: %w", err)
		}
		return 0, nil
	case adapters.StreamExists:
		oldVersion, ok, err := a.incrementExistingStream(ctx, streamID, nil, count, now)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, adapters.NewStreamNotFoundError(streamID)
		}
		return oldVersion, nil
	default:
		oldVersion, ok, err := a.incrementExistingStream(ctx, streamID, bson.M{"version": expectedVersion}, count, now)
		if err != nil {
			return 0, err
		}
		if !ok {
			actual := a.currentStreamVersion(ctx, streamID)
			return 0, adapters.NewConcurrencyError(streamID, expectedVersion, actual)
		}
		return oldVersion, nil
	}
}

func (a *MongoAdapter) incrementExistingStream(ctx context.Context, streamID string, extra bson.M, count int64, now time.Time) (int64, bool, error) {
	filter := bson.M{"_id": streamID}
	for k, v := range extra {
		filter[k] = v
	}

	update := bson.M{
		"$inc": bson.M{"version": count, "event_count": count},
		"$set": bson.M{"updated_at": now},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.Before)

	var old streamDoc
	err := a.streams().FindOneAndUpdate(ctx, filter, update, opts).Decode(&old)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("mink/mongodb: failed to update stream: %w", err)
	}
	return old.Version, true, nil
}

func (a *MongoAdapter) reservePositions(ctx context.Context, count int64) (int64, error) {
	update := bson.M{"$inc": bson.M{"value": count}}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)

	var counter counterDoc
	err := a.counters().FindOneAndUpdate(ctx, bson.M{"_id": "global_position"}, update, opts).Decode(&counter)
	if err != nil {
		return 0, fmt.Errorf("mink/mongodb: failed to reserve global positions: %w", err)
	}
	return counter.Value - count + 1, nil
}

func (a *MongoAdapter) currentStreamVersion(ctx context.Context, streamID string) int64 {
	var stream streamDoc
	if err := a.streams().FindOne(ctx, bson.M{"_id": streamID}).Decode(&stream); err == nil {
		return stream.Version
	}
	return 0
}

// Load retrieves all events from a stream starting from the specified version.
func (a *MongoAdapter) Load(ctx context.Context, streamID string, fromVersion int64) ([]adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}
	if streamID == "" {
		return nil, ErrEmptyStreamID
	}
	filter := bson.M{"stream_id": streamID, "version": bson.M{"$gt": fromVersion}}
	opts := options.Find().SetSort(bson.D{{Key: "version", Value: 1}})
	return a.findEvents(ctx, filter, opts)
}

// GetStreamInfo returns metadata about a stream.
func (a *MongoAdapter) GetStreamInfo(ctx context.Context, streamID string) (*adapters.StreamInfo, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}
	var stream streamDoc
	err := a.streams().FindOne(ctx, bson.M{"_id": streamID}).Decode(&stream)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, adapters.NewStreamNotFoundError(streamID)
	}
	if err != nil {
		return nil, fmt.Errorf("mink/mongodb: failed to get stream info: %w", err)
	}
	return &adapters.StreamInfo{
		StreamID:   stream.ID,
		Category:   stream.Category,
		Version:    stream.Version,
		EventCount: stream.EventCount,
		CreatedAt:  stream.CreatedAt,
		UpdatedAt:  stream.UpdatedAt,
	}, nil
}

// GetLastPosition returns the global position of the last stored event.
func (a *MongoAdapter) GetLastPosition(ctx context.Context) (uint64, error) {
	if a.closed {
		return 0, ErrAdapterClosed
	}
	var counter counterDoc
	err := a.counters().FindOne(ctx, bson.M{"_id": "global_position"}).Decode(&counter)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("mink/mongodb: failed to get last position: %w", err)
	}
	if counter.Value < 0 {
		return 0, fmt.Errorf("mink/mongodb: invalid negative global position: %d", counter.Value)
	}
	return uint64(counter.Value), nil
}

// Close releases resources held by the adapter.
func (a *MongoAdapter) Close() error {
	a.closed = true
	if a.ownsClient && a.client != nil {
		return a.client.Disconnect(context.Background())
	}
	return nil
}

// Ping checks MongoDB connectivity.
func (a *MongoAdapter) Ping(ctx context.Context) error {
	if a.closed {
		return ErrAdapterClosed
	}
	if err := a.client.Ping(ctx, nil); err != nil {
		return fmt.Errorf("mink/mongodb: ping failed: %w", err)
	}
	return nil
}

func (a *MongoAdapter) findEvents(ctx context.Context, filter any, opts ...options.Lister[options.FindOptions]) ([]adapters.StoredEvent, error) {
	cursor, err := a.events().Find(ctx, filter, opts...)
	if err != nil {
		return nil, fmt.Errorf("mink/mongodb: failed to load events: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var docs []eventDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("mink/mongodb: failed to decode events: %w", err)
	}

	events := make([]adapters.StoredEvent, len(docs))
	for i, doc := range docs {
		events[i] = storedEventFromDoc(doc)
	}
	return events, nil
}

func storedEventFromDoc(doc eventDoc) adapters.StoredEvent {
	return adapters.StoredEvent{
		ID:             doc.ID,
		StreamID:       doc.StreamID,
		Type:           doc.Type,
		Data:           doc.Data.Data,
		Metadata:       metadataFromDoc(doc.Metadata),
		Version:        doc.Version,
		GlobalPosition: uint64(doc.GlobalPosition),
		Timestamp:      doc.Timestamp,
	}
}

func metadataToDoc(m adapters.Metadata) metadataDoc {
	return metadataDoc{
		CorrelationID: m.CorrelationID,
		CausationID:   m.CausationID,
		UserID:        m.UserID,
		TenantID:      m.TenantID,
		Custom:        m.Custom,
	}
}

func metadataFromDoc(m metadataDoc) adapters.Metadata {
	return adapters.Metadata{
		CorrelationID: m.CorrelationID,
		CausationID:   m.CausationID,
		UserID:        m.UserID,
		TenantID:      m.TenantID,
		Custom:        m.Custom,
	}
}

func (a *MongoAdapter) resolveTransactionMode(ctx context.Context) error {
	switch a.transactionMode {
	case TransactionModeDisabled:
		a.transactionsActive = false
		return nil
	case TransactionModeAuto, TransactionModeRequired:
	default:
		return fmt.Errorf("mink/mongodb: invalid transaction mode %d", a.transactionMode)
	}

	err := a.probeTransactions(ctx)
	if err == nil {
		a.transactionsActive = true
		return nil
	}
	if a.transactionMode == TransactionModeRequired {
		return fmt.Errorf("mink/mongodb: transactions required but unavailable: %w", err)
	}
	a.transactionsActive = false
	return nil
}

func (a *MongoAdapter) probeTransactions(ctx context.Context) error {
	kind := a.deploymentKind(ctx)
	if kind == "standalone" {
		return fmt.Errorf("transactions require a replica set or sharded cluster")
	}

	session, err := a.client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)

	return mongo.WithSession(ctx, session, func(sc context.Context) error {
		if err := session.StartTransaction(a.effectiveTransactionOptions()...); err != nil {
			return err
		}
		return session.AbortTransaction(sc)
	})
}

func transactionOptionsWithConcerns(
	wc *writeconcern.WriteConcern,
	rc *readconcern.ReadConcern,
	rp *readpref.ReadPref,
	explicit []options.Lister[options.TransactionOptions],
) []options.Lister[options.TransactionOptions] {
	var opts []options.Lister[options.TransactionOptions]
	if wc != nil || rc != nil || rp != nil {
		builder := options.Transaction()
		if wc != nil {
			builder.SetWriteConcern(wc)
		}
		if rc != nil {
			builder.SetReadConcern(rc)
		}
		if rp != nil {
			builder.SetReadPreference(rp)
		}
		opts = append(opts, builder)
	}
	opts = append(opts, explicit...)
	return opts
}

func (a *MongoAdapter) initializeCounter(ctx context.Context) error {
	_, err := a.counters().UpdateOne(
		ctx,
		bson.M{"_id": "global_position"},
		bson.M{"$setOnInsert": bson.M{"value": int64(0)}},
		options.UpdateOne().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("mink/mongodb: failed to initialize counter: %w", err)
	}
	return nil
}

func (a *MongoAdapter) createIndexes(ctx context.Context) error {
	indexes := map[*mongo.Collection][]mongo.IndexModel{
		a.streams(): {
			{Keys: bson.D{{Key: "category", Value: 1}}, Options: options.Index().SetName("idx_streams_category")},
			{Keys: bson.D{{Key: "updated_at", Value: -1}}, Options: options.Index().SetName("idx_streams_updated_at")},
		},
		a.events(): {
			{Keys: bson.D{{Key: "stream_id", Value: 1}, {Key: "version", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uidx_events_stream_version")},
			{Keys: bson.D{{Key: "global_position", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uidx_events_global_position")},
			{Keys: bson.D{{Key: "type", Value: 1}}, Options: options.Index().SetName("idx_events_type")},
			{Keys: bson.D{{Key: "timestamp", Value: 1}}, Options: options.Index().SetName("idx_events_timestamp")},
			{Keys: bson.D{{Key: "stream_id", Value: 1}}, Options: options.Index().SetName("idx_events_stream")},
		},
		a.checkpoints(): {
			{Keys: bson.D{{Key: "status", Value: 1}}, Options: options.Index().SetName("idx_checkpoints_status")},
			{Keys: bson.D{{Key: "updated_at", Value: -1}}, Options: options.Index().SetName("idx_checkpoints_updated_at")},
		},
		a.migrations(): {
			{Keys: bson.D{{Key: "applied_at", Value: -1}}, Options: options.Index().SetName("idx_migrations_applied_at")},
		},
		a.idempotency(): {
			{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetName("ttl_idempotency_expires_at").SetExpireAfterSeconds(0)},
			{Keys: bson.D{{Key: "processed_at", Value: 1}}, Options: options.Index().SetName("idx_idempotency_processed_at")},
		},
		a.outbox(): {
			{Keys: bson.D{{Key: "status", Value: 1}, {Key: "scheduled_at", Value: 1}}, Options: options.Index().SetName("idx_outbox_pending")},
			{Keys: bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: -1}}, Options: options.Index().SetName("idx_outbox_dead_letter")},
		},
		a.sagas(): {
			{Keys: bson.D{{Key: "correlation_id", Value: 1}}, Options: options.Index().SetName("idx_sagas_correlation_id")},
			{Keys: bson.D{{Key: "type", Value: 1}}, Options: options.Index().SetName("idx_sagas_type")},
			{Keys: bson.D{{Key: "status", Value: 1}}, Options: options.Index().SetName("idx_sagas_status")},
			{Keys: bson.D{{Key: "type", Value: 1}, {Key: "status", Value: 1}}, Options: options.Index().SetName("idx_sagas_type_status")},
		},
		a.resumeTokens(): {
			{Keys: bson.D{{Key: "updated_at", Value: -1}}, Options: options.Index().SetName("idx_resume_tokens_updated_at")},
		},
	}

	for coll, models := range indexes {
		if _, err := coll.Indexes().CreateMany(ctx, models); err != nil {
			return fmt.Errorf("mink/mongodb: failed to create indexes for %s: %w", coll.Name(), err)
		}
	}
	return nil
}

func validateCollectionNames(names CollectionNames) error {
	values := map[string]string{
		"streams":       names.Streams,
		"events":        names.Events,
		"snapshots":     names.Snapshots,
		"checkpoints":   names.Checkpoints,
		"migrations":    names.Migrations,
		"idempotency":   names.Idempotency,
		"outbox":        names.Outbox,
		"sagas":         names.Sagas,
		"counters":      names.Counters,
		"resume tokens": names.ResumeTokens,
	}
	for kind, value := range values {
		if err := validateName(value, kind+" collection"); err != nil {
			return err
		}
	}
	return nil
}

func validateName(name, kind string) error {
	if name == "" {
		return fmt.Errorf("mink/mongodb: %s name is required", kind)
	}
	if strings.ContainsAny(name, "\x00$") {
		return fmt.Errorf("mink/mongodb: %s name contains invalid characters", kind)
	}
	return nil
}

func mergeCollectionNames(base, override CollectionNames) CollectionNames {
	if override.Streams != "" {
		base.Streams = override.Streams
	}
	if override.Events != "" {
		base.Events = override.Events
	}
	if override.Snapshots != "" {
		base.Snapshots = override.Snapshots
	}
	if override.Checkpoints != "" {
		base.Checkpoints = override.Checkpoints
	}
	if override.Migrations != "" {
		base.Migrations = override.Migrations
	}
	if override.Idempotency != "" {
		base.Idempotency = override.Idempotency
	}
	if override.Outbox != "" {
		base.Outbox = override.Outbox
	}
	if override.Sagas != "" {
		base.Sagas = override.Sagas
	}
	if override.Counters != "" {
		base.Counters = override.Counters
	}
	if override.ResumeTokens != "" {
		base.ResumeTokens = override.ResumeTokens
	}
	return base
}

func prefixCollections(names CollectionNames, prefix string) CollectionNames {
	return CollectionNames{
		Streams:      prefix + names.Streams,
		Events:       prefix + names.Events,
		Snapshots:    prefix + names.Snapshots,
		Checkpoints:  prefix + names.Checkpoints,
		Migrations:   prefix + names.Migrations,
		Idempotency:  prefix + names.Idempotency,
		Outbox:       prefix + names.Outbox,
		Sagas:        prefix + names.Sagas,
		Counters:     prefix + names.Counters,
		ResumeTokens: prefix + names.ResumeTokens,
	}
}

package mongodb

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"go-mink.dev/adapters"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

const (
	defaultPollInterval       = 100 * time.Millisecond
	defaultSubscriptionBuffer = 100
	defaultMaxRetries         = 5
)

type snapshotDoc struct {
	ID        string      `bson:"_id"`
	Version   int64       `bson:"version"`
	Data      bson.Binary `bson:"data"`
	CreatedAt time.Time   `bson:"created_at"`
}

type checkpointDoc struct {
	ID        string    `bson:"_id"`
	Position  int64     `bson:"position"`
	Status    string    `bson:"status"`
	UpdatedAt time.Time `bson:"updated_at"`
}

type migrationDoc struct {
	ID        string    `bson:"_id"`
	AppliedAt time.Time `bson:"applied_at"`
}

// LoadFromPosition loads events starting from a global position.
func (a *MongoAdapter) LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}
	limit = adapters.DefaultLimit(limit, 1000)
	filter := bson.M{"global_position": bson.M{"$gt": int64(fromPosition)}}
	opts := options.Find().SetSort(bson.D{{Key: "global_position", Value: 1}}).SetLimit(int64(limit))
	return a.findEvents(ctx, filter, opts)
}

// SubscribeAll subscribes to all events across all streams.
func (a *MongoAdapter) SubscribeAll(ctx context.Context, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}
	cfg := newSubscriptionConfig(opts)
	ch := make(chan adapters.StoredEvent, cfg.bufferSize)

	loader := func(ctx context.Context, position uint64) ([]adapters.StoredEvent, uint64, error) {
		events, err := a.LoadFromPosition(ctx, position, cfg.bufferSize)
		if err != nil {
			return nil, position, err
		}
		if len(events) == 0 {
			return events, position, nil
		}
		return events, events[len(events)-1].GlobalPosition, nil
	}
	if err := startSubscription(a, ctx, cfg, ch, "SubscribeAll", fromPosition, loader, bson.M{}); err != nil {
		return nil, err
	}
	return ch, nil
}

// SubscribeStream subscribes to events from a specific stream.
func (a *MongoAdapter) SubscribeStream(ctx context.Context, streamID string, fromVersion int64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}
	cfg := newSubscriptionConfig(opts)
	ch := make(chan adapters.StoredEvent, cfg.bufferSize)

	loader := func(ctx context.Context, version int64) ([]adapters.StoredEvent, int64, error) {
		events, err := a.Load(ctx, streamID, version)
		if err != nil {
			return nil, version, err
		}
		if len(events) == 0 {
			return events, version, nil
		}
		return events, events[len(events)-1].Version, nil
	}
	changeFilter := bson.M{"stream_id": streamID}
	if err := startSubscription(a, ctx, cfg, ch, fmt.Sprintf("SubscribeStream(%s)", streamID), fromVersion, loader, changeFilter); err != nil {
		return nil, err
	}
	return ch, nil
}

// SubscribeCategory subscribes to all events from streams in a category.
func (a *MongoAdapter) SubscribeCategory(ctx context.Context, category string, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}
	cfg := newSubscriptionConfig(opts)
	ch := make(chan adapters.StoredEvent, cfg.bufferSize)

	loader := func(ctx context.Context, position uint64) ([]adapters.StoredEvent, uint64, error) {
		filter := bson.M{
			"global_position": bson.M{"$gt": int64(position)},
			"stream_id":       bson.M{"$regex": "^" + regexpQuote(category) + "-"},
		}
		findOpts := options.Find().SetSort(bson.D{{Key: "global_position", Value: 1}}).SetLimit(int64(cfg.bufferSize))
		events, err := a.findEvents(ctx, filter, findOpts)
		if err != nil {
			return nil, position, err
		}
		if len(events) == 0 {
			return events, position, nil
		}
		return events, events[len(events)-1].GlobalPosition, nil
	}
	changeFilter := bson.M{"stream_id": bson.M{"$regex": "^" + regexpQuote(category) + "-"}}
	if err := startSubscription(a, ctx, cfg, ch, fmt.Sprintf("SubscribeCategory(%s)", category), fromPosition, loader, changeFilter); err != nil {
		return nil, err
	}
	return ch, nil
}

// SaveSnapshot stores a snapshot for the given stream.
func (a *MongoAdapter) SaveSnapshot(ctx context.Context, streamID string, version int64, data []byte) error {
	if a.closed {
		return ErrAdapterClosed
	}
	doc := snapshotDoc{
		ID:        streamID,
		Version:   version,
		Data:      bson.Binary{Subtype: bson.TypeBinaryGeneric, Data: data},
		CreatedAt: time.Now().UTC(),
	}
	_, err := a.snapshots().ReplaceOne(ctx, bson.M{"_id": streamID}, doc, options.Replace().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("mink/mongodb: failed to save snapshot: %w", err)
	}
	return nil
}

// LoadSnapshot retrieves the latest snapshot for the given stream.
func (a *MongoAdapter) LoadSnapshot(ctx context.Context, streamID string) (*adapters.SnapshotRecord, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}
	var doc snapshotDoc
	err := a.snapshots().FindOne(ctx, bson.M{"_id": streamID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mink/mongodb: failed to load snapshot: %w", err)
	}
	return &adapters.SnapshotRecord{StreamID: streamID, Version: doc.Version, Data: doc.Data.Data}, nil
}

// DeleteSnapshot removes the snapshot for the given stream.
func (a *MongoAdapter) DeleteSnapshot(ctx context.Context, streamID string) error {
	if a.closed {
		return ErrAdapterClosed
	}
	_, err := a.snapshots().DeleteOne(ctx, bson.M{"_id": streamID})
	return err
}

// GetCheckpoint returns the last processed position for a projection.
func (a *MongoAdapter) GetCheckpoint(ctx context.Context, projectionName string) (uint64, error) {
	if a.closed {
		return 0, ErrAdapterClosed
	}
	var doc checkpointDoc
	err := a.checkpoints().FindOne(ctx, bson.M{"_id": projectionName}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("mink/mongodb: failed to get checkpoint: %w", err)
	}
	if doc.Position < 0 {
		return 0, fmt.Errorf("mink/mongodb: invalid negative checkpoint: %d", doc.Position)
	}
	return uint64(doc.Position), nil
}

// SetCheckpoint stores the last processed position for a projection.
func (a *MongoAdapter) SetCheckpoint(ctx context.Context, projectionName string, position uint64) error {
	if a.closed {
		return ErrAdapterClosed
	}
	update := bson.M{
		"$set": bson.M{
			"position":   int64(position),
			"status":     "active",
			"updated_at": time.Now().UTC(),
		},
	}
	_, err := a.checkpoints().UpdateOne(ctx, bson.M{"_id": projectionName}, update, options.UpdateOne().SetUpsert(true))
	return err
}

// DeleteCheckpoint removes the checkpoint for a projection.
func (a *MongoAdapter) DeleteCheckpoint(ctx context.Context, projectionName string) error {
	if a.closed {
		return ErrAdapterClosed
	}
	_, err := a.checkpoints().DeleteOne(ctx, bson.M{"_id": projectionName})
	return err
}

// GetAllCheckpoints returns checkpoints for all projections.
func (a *MongoAdapter) GetAllCheckpoints(ctx context.Context) (map[string]uint64, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}
	cursor, err := a.checkpoints().Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	result := make(map[string]uint64)
	for cursor.Next(ctx) {
		var doc checkpointDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		if doc.Position >= 0 {
			result[doc.ID] = uint64(doc.Position)
		}
	}
	return result, cursor.Err()
}

// ListStreams returns a list of stream summaries.
func (a *MongoAdapter) ListStreams(ctx context.Context, prefix string, limit int) ([]adapters.StreamSummary, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}
	filter := bson.M{}
	if prefix != "" {
		filter["_id"] = bson.M{"$regex": "^" + regexpQuote(prefix)}
	}
	opts := options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	cursor, err := a.streams().Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var summaries []adapters.StreamSummary
	for cursor.Next(ctx) {
		var stream streamDoc
		if err := cursor.Decode(&stream); err != nil {
			return nil, err
		}
		lastType := ""
		var last eventDoc
		err := a.events().FindOne(
			ctx,
			bson.M{"stream_id": stream.ID},
			options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}}),
		).Decode(&last)
		if err == nil {
			lastType = last.Type
		}
		summaries = append(summaries, adapters.StreamSummary{
			StreamID:      stream.ID,
			EventCount:    stream.EventCount,
			LastEventType: lastType,
			LastUpdated:   stream.UpdatedAt,
		})
	}
	return summaries, cursor.Err()
}

// GetStreamEvents returns events from a stream with pagination.
func (a *MongoAdapter) GetStreamEvents(ctx context.Context, streamID string, fromVersion int64, limit int) ([]adapters.StoredEvent, error) {
	if limit <= 0 {
		return a.Load(ctx, streamID, fromVersion)
	}
	filter := bson.M{"stream_id": streamID, "version": bson.M{"$gt": fromVersion}}
	opts := options.Find().SetSort(bson.D{{Key: "version", Value: 1}}).SetLimit(int64(limit))
	return a.findEvents(ctx, filter, opts)
}

// GetEventStoreStats returns aggregate statistics about the event store.
func (a *MongoAdapter) GetEventStoreStats(ctx context.Context) (*adapters.EventStoreStats, error) {
	totalEvents, err := a.events().CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	totalStreams, err := a.streams().CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, err
	}

	stats := &adapters.EventStoreStats{TotalEvents: totalEvents, TotalStreams: totalStreams}
	if totalStreams > 0 {
		stats.AvgEventsPerStream = float64(totalEvents) / float64(totalStreams)
	}

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$type"}, {Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}}}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "count", Value: -1}}}},
		bson.D{{Key: "$limit", Value: 5}},
	}
	cursor, err := a.events().Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	for cursor.Next(ctx) {
		var row struct {
			Type  string `bson:"_id"`
			Count int64  `bson:"count"`
		}
		if err := cursor.Decode(&row); err != nil {
			return nil, err
		}
		stats.TopEventTypes = append(stats.TopEventTypes, adapters.EventTypeCount{Type: row.Type, Count: row.Count})
	}
	stats.EventTypes = int64(len(stats.TopEventTypes))
	return stats, cursor.Err()
}

// ListProjections returns all registered projections.
func (a *MongoAdapter) ListProjections(ctx context.Context) ([]adapters.ProjectionInfo, error) {
	cursor, err := a.checkpoints().Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var projections []adapters.ProjectionInfo
	for cursor.Next(ctx) {
		var doc checkpointDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		projections = append(projections, projectionInfoFromDoc(doc))
	}
	return projections, cursor.Err()
}

// GetProjection returns information about a specific projection.
func (a *MongoAdapter) GetProjection(ctx context.Context, name string) (*adapters.ProjectionInfo, error) {
	var doc checkpointDoc
	err := a.checkpoints().FindOne(ctx, bson.M{"_id": name}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info := projectionInfoFromDoc(doc)
	return &info, nil
}

// SetProjectionStatus updates a projection's status.
func (a *MongoAdapter) SetProjectionStatus(ctx context.Context, name string, status string) error {
	res, err := a.checkpoints().UpdateOne(ctx, bson.M{"_id": name}, bson.M{"$set": bson.M{"status": status, "updated_at": time.Now().UTC()}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("mink/mongodb: projection %q not found", name)
	}
	return nil
}

// ResetProjectionCheckpoint resets a projection's position to 0 for rebuild.
func (a *MongoAdapter) ResetProjectionCheckpoint(ctx context.Context, name string) error {
	_, err := a.checkpoints().UpdateOne(ctx, bson.M{"_id": name}, bson.M{"$set": bson.M{"position": int64(0), "updated_at": time.Now().UTC()}})
	return err
}

// GetTotalEventCount returns the highest global position.
func (a *MongoAdapter) GetTotalEventCount(ctx context.Context) (int64, error) {
	pos, err := a.GetLastPosition(ctx)
	return int64(pos), err
}

// GetAppliedMigrations returns the list of applied migration names.
func (a *MongoAdapter) GetAppliedMigrations(ctx context.Context) ([]string, error) {
	cursor, err := a.migrations().Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()
	var names []string
	for cursor.Next(ctx) {
		var doc migrationDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		names = append(names, doc.ID)
	}
	return names, cursor.Err()
}

// RecordMigration marks a migration as applied.
func (a *MongoAdapter) RecordMigration(ctx context.Context, name string) error {
	_, err := a.migrations().ReplaceOne(ctx, bson.M{"_id": name}, migrationDoc{ID: name, AppliedAt: time.Now().UTC()}, options.Replace().SetUpsert(true))
	return err
}

// RemoveMigrationRecord removes a migration record.
func (a *MongoAdapter) RemoveMigrationRecord(ctx context.Context, name string) error {
	_, err := a.migrations().DeleteOne(ctx, bson.M{"_id": name})
	return err
}

// ExecuteSQL returns an unsupported error because MongoDB does not execute SQL migrations.
func (a *MongoAdapter) ExecuteSQL(ctx context.Context, sql string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("mink/mongodb: SQL migrations are not supported; MongoDB schema is created with Initialize")
}

// GetDiagnosticInfo returns MongoDB version and connection status.
func (a *MongoAdapter) GetDiagnosticInfo(ctx context.Context) (*adapters.DiagnosticInfo, error) {
	info := &adapters.DiagnosticInfo{
		Details:  map[string]string{},
		Warnings: []string{},
	}
	if err := a.Ping(ctx); err != nil {
		info.Connected = false
		info.Message = err.Error()
		return info, nil
	}
	info.Connected = true
	var result struct {
		Version string `bson:"version"`
	}
	if err := a.db.RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&result); err != nil {
		info.Message = "Connected but couldn't get MongoDB version"
		return info, nil
	}
	info.Version = "MongoDB " + result.Version
	deploymentKind := a.deploymentKind(ctx)
	info.Details["deployment"] = deploymentKind
	info.Details["transaction_mode"] = transactionModeString(a.transactionMode)
	info.Details["transactions_active"] = fmt.Sprint(a.transactionsActive)
	info.Details["transactions_available"] = fmt.Sprint(a.probeTransactions(ctx) == nil)
	info.Details["subscription_mode"] = subscriptionModeString(a.subscriptionMode)
	info.Details["change_streams_available"] = fmt.Sprint(a.probeChangeStreams(ctx) == nil)
	info.Details["write_concern"] = writeConcernString(a.writeConcern)
	info.Details["read_concern"] = readConcernString(a.readConcern)
	info.Details["read_preference"] = readPreferenceString(a.readPreference)
	info.Warnings = append(info.Warnings, a.indexDriftWarnings(ctx)...)
	info.Warnings = append(info.Warnings, a.shardingWarnings(ctx)...)
	if a.transactionMode == TransactionModeRequired && !a.transactionsActive {
		info.Warnings = append(info.Warnings, "transactions are required but not active")
	}
	if a.subscriptionMode == SubscriptionModeChangeStream && info.Details["change_streams_available"] != "true" {
		info.Warnings = append(info.Warnings, "change-stream subscriptions are required but unavailable")
	}
	mode := "standalone/best-effort"
	if a.transactionsActive {
		mode = "transactions"
	}
	info.Message = "Connected successfully (" + mode + ", " + deploymentKind + ")"
	return info, nil
}

func (a *MongoAdapter) deploymentKind(ctx context.Context) string {
	var hello struct {
		SetName string `bson:"setName"`
		Msg     string `bson:"msg"`
	}
	if err := a.db.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		return "unknown"
	}
	if hello.Msg == "isdbgrid" {
		return "sharded"
	}
	if hello.SetName != "" {
		return "replica_set"
	}
	return "standalone"
}

func (a *MongoAdapter) probeChangeStreams(ctx context.Context) error {
	stream, err := a.watchEvents(ctx, bson.M{}, nil)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close(ctx) }()
	return probeChangeStream(ctx, stream)
}

func (a *MongoAdapter) indexDriftWarnings(ctx context.Context) []string {
	expected := map[*mongo.Collection][]string{
		a.events():       {"uidx_events_stream_version", "uidx_events_global_position", "idx_events_type", "idx_events_timestamp", "idx_events_stream"},
		a.streams():      {"idx_streams_category", "idx_streams_updated_at"},
		a.checkpoints():  {"idx_checkpoints_status", "idx_checkpoints_updated_at"},
		a.resumeTokens(): {"idx_resume_tokens_updated_at"},
	}
	var warnings []string
	for coll, names := range expected {
		specs, err := coll.Indexes().ListSpecifications(ctx)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not inspect indexes for %s: %v", coll.Name(), err))
			continue
		}
		present := map[string]bool{}
		for _, spec := range specs {
			present[spec.Name] = true
		}
		for _, name := range names {
			if !present[name] {
				warnings = append(warnings, fmt.Sprintf("missing index %s on %s", name, coll.Name()))
			}
		}
	}
	return warnings
}

func (a *MongoAdapter) shardingWarnings(ctx context.Context) []string {
	var warnings []string
	for _, collection := range []string{a.collections.Events, a.collections.Streams, a.collections.Checkpoints, a.collections.Outbox, a.collections.Sagas} {
		var stats struct {
			Sharded bool `bson:"sharded"`
		}
		if err := a.db.RunCommand(ctx, bson.D{{Key: "collStats", Value: collection}}).Decode(&stats); err != nil {
			continue
		}
		if stats.Sharded {
			warnings = append(warnings, fmt.Sprintf("collection %s is sharded; verify unique indexes include the shard key prefix", collection))
		}
	}
	return warnings
}

func transactionModeString(mode TransactionMode) string {
	switch mode {
	case TransactionModeAuto:
		return "auto"
	case TransactionModeRequired:
		return "required"
	case TransactionModeDisabled:
		return "disabled"
	default:
		return fmt.Sprintf("unknown(%d)", mode)
	}
}

func subscriptionModeString(mode SubscriptionMode) string {
	switch mode {
	case SubscriptionModeAuto:
		return "auto"
	case SubscriptionModePolling:
		return "polling"
	case SubscriptionModeChangeStream:
		return "change_stream"
	default:
		return fmt.Sprintf("unknown(%d)", mode)
	}
}

func writeConcernString(wc *writeconcern.WriteConcern) string {
	if wc == nil {
		return "default"
	}
	if !wc.Acknowledged() {
		return "unacknowledged"
	}
	if wc.W == nil {
		return "acknowledged"
	}
	return fmt.Sprint(wc.W)
}

func readConcernString(rc *readconcern.ReadConcern) string {
	if rc == nil {
		return "default"
	}
	return rc.Level
}

func readPreferenceString(rp *readpref.ReadPref) string {
	if rp == nil {
		return "default"
	}
	return rp.Mode().String()
}

// CheckSchema verifies the event store collection exists.
func (a *MongoAdapter) CheckSchema(ctx context.Context, tableName string) (*adapters.SchemaCheckResult, error) {
	collectionName := tableName
	if collectionName == "" {
		collectionName = a.collections.Events
	}
	names, err := a.db.ListCollectionNames(ctx, bson.M{"name": collectionName})
	if err != nil {
		return nil, err
	}
	result := &adapters.SchemaCheckResult{TableExists: len(names) > 0}
	if !result.TableExists {
		result.Message = fmt.Sprintf("Collection %q not found", collectionName)
		return result, nil
	}
	count, err := a.collection(collectionName).CountDocuments(ctx, bson.M{})
	if err != nil {
		result.Message = fmt.Sprintf("Collection exists but couldn't count events: %v", err)
		return result, nil
	}
	result.EventCount = count
	result.Message = fmt.Sprintf("Collection exists (%d events)", count)
	return result, nil
}

// GetProjectionHealth returns projection health status.
func (a *MongoAdapter) GetProjectionHealth(ctx context.Context) (*adapters.ProjectionHealthResult, error) {
	total, err := a.checkpoints().CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	max, err := a.GetTotalEventCount(ctx)
	if err != nil {
		return nil, err
	}
	behind, err := a.checkpoints().CountDocuments(ctx, bson.M{"position": bson.M{"$lt": max}})
	if err != nil {
		return nil, err
	}
	result := &adapters.ProjectionHealthResult{
		TotalProjections:  total,
		ProjectionsBehind: behind,
		MaxPosition:       max,
	}
	if total == 0 {
		result.Message = "No projections registered"
	} else if behind > 0 {
		result.Message = fmt.Sprintf("%d/%d projections behind", behind, total)
	} else {
		result.Message = fmt.Sprintf("%d projections, all up to date", total)
	}
	return result, nil
}

func projectionInfoFromDoc(doc checkpointDoc) adapters.ProjectionInfo {
	status := doc.Status
	if status == "" {
		status = "active"
	}
	return adapters.ProjectionInfo{
		Name:      doc.ID,
		Position:  doc.Position,
		Status:    status,
		UpdatedAt: doc.UpdatedAt,
	}
}

type subscriptionConfig struct {
	bufferSize       int
	pollInterval     time.Duration
	maxRetries       int
	onError          func(error)
	resumeTokenKey   string
	resetResumeToken bool
	resumeTokenStore *ResumeTokenStore
}

func newSubscriptionConfig(opts []adapters.SubscriptionOptions) subscriptionConfig {
	cfg := subscriptionConfig{
		bufferSize:   defaultSubscriptionBuffer,
		pollInterval: defaultPollInterval,
		maxRetries:   defaultMaxRetries,
	}
	if len(opts) > 0 {
		if opts[0].BufferSize > 0 {
			cfg.bufferSize = opts[0].BufferSize
		}
		if opts[0].PollInterval > 0 {
			cfg.pollInterval = opts[0].PollInterval
		}
		cfg.onError = opts[0].OnError
		cfg.resumeTokenKey = opts[0].ResumeTokenKey
		cfg.resetResumeToken = opts[0].ResetResumeToken
	}
	return cfg
}

type eventLoader[P any] func(context.Context, P) ([]adapters.StoredEvent, P, error)

func startSubscription[P any](
	a *MongoAdapter,
	ctx context.Context,
	cfg subscriptionConfig,
	ch chan adapters.StoredEvent,
	label string,
	initial P,
	loader eventLoader[P],
	changeFilter bson.M,
) error {
	if cfg.resumeTokenKey != "" {
		cfg.resumeTokenStore = a.resumeTokenStore
	}
	switch a.subscriptionMode {
	case SubscriptionModePolling:
		go runPollingLoop(ctx, cfg, ch, label, initial, loader)
		return nil
	case SubscriptionModeAuto, SubscriptionModeChangeStream:
	default:
		close(ch)
		return fmt.Errorf("mink/mongodb: invalid subscription mode %d", a.subscriptionMode)
	}

	stream, err := a.openChangeStream(ctx, cfg, changeFilter)
	if err != nil {
		if a.subscriptionMode == SubscriptionModeChangeStream {
			close(ch)
			return fmt.Errorf("mink/mongodb: failed to start change stream subscription: %w", err)
		}
		reportSubscriptionError(cfg, label, fmt.Errorf("change stream unavailable, falling back to polling: %w", err))
		go runPollingLoop(ctx, cfg, ch, label, initial, loader)
		return nil
	}
	if err := probeChangeStream(ctx, stream); err != nil {
		_ = stream.Close(ctx)
		if a.subscriptionMode == SubscriptionModeChangeStream {
			close(ch)
			return fmt.Errorf("mink/mongodb: failed to start change stream subscription: %w", err)
		}
		reportSubscriptionError(cfg, label, fmt.Errorf("change stream unavailable, falling back to polling: %w", err))
		go runPollingLoop(ctx, cfg, ch, label, initial, loader)
		return nil
	}
	go runChangeStreamLoop(ctx, cfg, ch, label, initial, loader, stream, a.subscriptionMode == SubscriptionModeAuto)
	return nil
}

func (a *MongoAdapter) openChangeStream(ctx context.Context, cfg subscriptionConfig, filter bson.M) (*mongo.ChangeStream, error) {
	var token bson.Raw
	if cfg.resumeTokenKey != "" && cfg.resumeTokenStore != nil {
		if cfg.resetResumeToken {
			if err := cfg.resumeTokenStore.Delete(ctx, cfg.resumeTokenKey); err != nil {
				return nil, err
			}
		} else {
			loaded, err := cfg.resumeTokenStore.Load(ctx, cfg.resumeTokenKey)
			if err != nil {
				return nil, err
			}
			token = loaded
		}
	}

	stream, err := a.watchEvents(ctx, filter, resumeAfterToken(token))
	if err != nil {
		if len(token) == 0 || cfg.resumeTokenStore == nil {
			return nil, err
		}
		if deleteErr := cfg.resumeTokenStore.Delete(ctx, cfg.resumeTokenKey); deleteErr != nil {
			return nil, deleteErr
		}
		stream, err = a.watchEvents(ctx, filter, nil)
		if err != nil {
			return nil, err
		}
	}
	if err := probeChangeStream(ctx, stream); err != nil {
		_ = stream.Close(ctx)
		if len(token) == 0 || cfg.resumeTokenStore == nil {
			return nil, err
		}
		if deleteErr := cfg.resumeTokenStore.Delete(ctx, cfg.resumeTokenKey); deleteErr != nil {
			return nil, deleteErr
		}
		stream, err = a.watchEvents(ctx, filter, nil)
		if err != nil {
			return nil, err
		}
		if err := probeChangeStream(ctx, stream); err != nil {
			_ = stream.Close(ctx)
			return nil, err
		}
	}
	if err := persistResumeToken(ctx, cfg, stream); err != nil {
		reportSubscriptionError(cfg, "resume token", err)
	}
	return stream, nil
}

func (a *MongoAdapter) watchEvents(ctx context.Context, filter bson.M, resumeAfter any) (*mongo.ChangeStream, error) {
	match := bson.D{{Key: "operationType", Value: "insert"}}
	for key, value := range filter {
		match = append(match, bson.E{Key: "fullDocument." + key, Value: value})
	}
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: match}},
	}
	opts := options.ChangeStream().SetMaxAwaitTime(defaultPollInterval)
	if resumeAfter != nil {
		opts.SetResumeAfter(resumeAfter)
	}
	return a.events().Watch(ctx, pipeline, opts)
}

func resumeAfterToken(token bson.Raw) any {
	if len(token) == 0 {
		return nil
	}
	return token
}

func runPollingLoop[P any](ctx context.Context, cfg subscriptionConfig, ch chan adapters.StoredEvent, label string, initial P, loader eventLoader[P]) {
	defer close(ch)
	_ = runPollingLoopCore(ctx, cfg, ch, label, initial, loader)
}

func runPollingLoopCore[P any](ctx context.Context, cfg subscriptionConfig, ch chan adapters.StoredEvent, label string, initial P, loader eventLoader[P]) P {
	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()

	position := initial
	consecutiveErrors := 0
	for {
		select {
		case <-ctx.Done():
			return position
		default:
		}

		events, newPosition, err := loader(ctx, position)
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= cfg.maxRetries {
				reportSubscriptionError(cfg, label, err)
				consecutiveErrors = 0
			}
			select {
			case <-ctx.Done():
				return position
			case <-ticker.C:
				continue
			}
		}
		consecutiveErrors = 0

		for _, event := range events {
			select {
			case ch <- event:
			case <-ctx.Done():
				return position
			}
		}
		if len(events) > 0 {
			position = newPosition
			continue
		}
		select {
		case <-ctx.Done():
			return position
		case <-ticker.C:
		}
	}
}

func runChangeStreamLoop[P any](
	ctx context.Context,
	cfg subscriptionConfig,
	ch chan adapters.StoredEvent,
	label string,
	initial P,
	loader eventLoader[P],
	stream *mongo.ChangeStream,
	autoFallback bool,
) {
	defer close(ch)
	defer func() { _ = stream.Close(ctx) }()

	position, ok := drainSubscriptionEvents(ctx, cfg, ch, label, initial, loader)
	if !ok {
		return
	}

	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()

	for {
		if stream.TryNext(ctx) {
			if err := persistResumeToken(ctx, cfg, stream); err != nil {
				reportSubscriptionError(cfg, label, err)
			}
			var keepGoing bool
			position, keepGoing = drainSubscriptionEvents(ctx, cfg, ch, label, position, loader)
			if !keepGoing {
				return
			}
			continue
		}
		if err := stream.Err(); err != nil {
			reportSubscriptionError(cfg, label, err)
			if autoFallback {
				_ = runPollingLoopCore(ctx, cfg, ch, label, position, loader)
			}
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var keepGoing bool
			position, keepGoing = drainSubscriptionEvents(ctx, cfg, ch, label, position, loader)
			if !keepGoing {
				return
			}
		}
	}
}

func drainSubscriptionEvents[P any](ctx context.Context, cfg subscriptionConfig, ch chan adapters.StoredEvent, label string, position P, loader eventLoader[P]) (P, bool) {
	for {
		events, newPosition, err := loader(ctx, position)
		if err != nil {
			reportSubscriptionError(cfg, label, err)
			return position, true
		}
		for _, event := range events {
			select {
			case ch <- event:
			case <-ctx.Done():
				return position, false
			}
		}
		if len(events) == 0 {
			return position, true
		}
		position = newPosition
	}
}

func probeChangeStream(ctx context.Context, stream *mongo.ChangeStream) error {
	_ = stream.TryNext(ctx)
	return stream.Err()
}

func persistResumeToken(ctx context.Context, cfg subscriptionConfig, stream *mongo.ChangeStream) error {
	if cfg.resumeTokenKey == "" || cfg.resumeTokenStore == nil {
		return nil
	}
	token := stream.ResumeToken()
	if len(token) == 0 {
		return nil
	}
	return cfg.resumeTokenStore.Save(ctx, cfg.resumeTokenKey, token)
}

func reportSubscriptionError(cfg subscriptionConfig, label string, err error) {
	if cfg.onError != nil {
		cfg.onError(err)
		return
	}
	log.Printf("mink/mongodb: subscription error (%s): %v", label, err)
}

func regexpQuote(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`.`, `\.`,
		`+`, `\+`,
		`*`, `\*`,
		`?`, `\?`,
		`(`, `\(`,
		`)`, `\)`,
		`[`, `\[`,
		`]`, `\]`,
		`{`, `\{`,
		`}`, `\}`,
		`^`, `\^`,
		`$`, `\$`,
		`|`, `\|`,
	)
	return replacer.Replace(value)
}

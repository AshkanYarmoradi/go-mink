package mongodb

import (
	"fmt"
	"strings"
)

// GenerateSchema returns a mongosh setup script for the MongoDB adapter.
func (a *MongoAdapter) GenerateSchema(projectName, tableName, snapshotTableName, outboxTableName string) string {
	names := a.collections
	if tableName != "" {
		names.Events = tableName
	}
	if snapshotTableName != "" {
		names.Snapshots = snapshotTableName
	}
	if outboxTableName != "" {
		names.Outbox = outboxTableName
	}
	return GenerateSchema(projectName, a.database, names)
}

// GenerateSchema returns a mongosh setup script aligned with the runtime adapter indexes.
func GenerateSchema(projectName, database string, names CollectionNames) string {
	if database == "" {
		database = "mink"
	}
	names = mergeCollectionNames(DefaultCollectionNames(), names)

	var b strings.Builder
	fmt.Fprintf(&b, `// Mink Event Store Schema (MongoDB)
// Generated for: %s

use(%q);

`, projectName, database)

	for _, name := range []string{
		names.Streams,
		names.Events,
		names.Snapshots,
		names.Checkpoints,
		names.Migrations,
		names.Idempotency,
		names.Outbox,
		names.Sagas,
		names.Counters,
	} {
		fmt.Fprintf(&b, "db.createCollection(%q);\n", name)
	}

	fmt.Fprintf(&b, `
db.%s.createIndex({ category: 1 }, { name: "idx_streams_category" });
db.%s.createIndex({ updated_at: -1 }, { name: "idx_streams_updated_at" });

db.%s.createIndex({ stream_id: 1, version: 1 }, { unique: true, name: "uidx_events_stream_version" });
db.%s.createIndex({ global_position: 1 }, { unique: true, name: "uidx_events_global_position" });
db.%s.createIndex({ type: 1 }, { name: "idx_events_type" });
db.%s.createIndex({ timestamp: 1 }, { name: "idx_events_timestamp" });
db.%s.createIndex({ stream_id: 1 }, { name: "idx_events_stream" });

db.%s.createIndex({ status: 1 }, { name: "idx_checkpoints_status" });
db.%s.createIndex({ updated_at: -1 }, { name: "idx_checkpoints_updated_at" });

db.%s.createIndex({ applied_at: -1 }, { name: "idx_migrations_applied_at" });

db.%s.createIndex({ expires_at: 1 }, { expireAfterSeconds: 0, name: "ttl_idempotency_expires_at" });
db.%s.createIndex({ processed_at: 1 }, { name: "idx_idempotency_processed_at" });

db.%s.createIndex({ status: 1, scheduled_at: 1 }, { name: "idx_outbox_pending" });
db.%s.createIndex({ status: 1, created_at: -1 }, { name: "idx_outbox_dead_letter" });

db.%s.createIndex({ correlation_id: 1 }, { name: "idx_sagas_correlation_id" });
db.%s.createIndex({ type: 1 }, { name: "idx_sagas_type" });
db.%s.createIndex({ status: 1 }, { name: "idx_sagas_status" });
db.%s.createIndex({ type: 1, status: 1 }, { name: "idx_sagas_type_status" });

db.%s.updateOne({ _id: "global_position" }, { $setOnInsert: { value: NumberLong(0) } }, { upsert: true });
`,
		names.Streams, names.Streams,
		names.Events, names.Events, names.Events, names.Events, names.Events,
		names.Checkpoints, names.Checkpoints,
		names.Migrations,
		names.Idempotency, names.Idempotency,
		names.Outbox, names.Outbox,
		names.Sagas, names.Sagas, names.Sagas, names.Sagas,
		names.Counters,
	)

	return b.String()
}

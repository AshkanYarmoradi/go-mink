---
title: Security
sidebar_position: 9
---

# Security & Compliance

---

## Field-Level Encryption

Protect sensitive PII data in events with field-level encryption. go-mink uses envelope encryption for performance: one provider call per event generates a data encryption key (DEK), then individual fields are encrypted locally with AES-256-GCM.

### Encryption Provider Interface

```go
package encryption

// Provider abstracts crypto operations for field-level encryption.
// Three built-in implementations: local (testing), AWS KMS, HashiCorp Vault.
type Provider interface {
    Encrypt(ctx context.Context, keyID string, plaintext []byte) (ciphertext []byte, err error)
    Decrypt(ctx context.Context, keyID string, ciphertext []byte) (plaintext []byte, err error)
    GenerateDataKey(ctx context.Context, keyID string) (*DataKey, error)
    DecryptDataKey(ctx context.Context, keyID string, encryptedKey []byte) ([]byte, error)
    Close() error
}

// DataKey holds plaintext (in-memory only) + ciphertext (safe to persist) of a DEK.
type DataKey struct {
    Plaintext  []byte // 32-byte AES key -- NEVER persisted, zeroed after use
    Ciphertext []byte // Encrypted DEK, stored in event metadata
    KeyID      string // Master key that encrypted the DEK
}

// Sentinel errors
var ErrEncryptionFailed = errors.New("mink: encryption failed")
var ErrDecryptionFailed = errors.New("mink: decryption failed")
var ErrKeyNotFound      = errors.New("mink: encryption key not found")
var ErrKeyRevoked       = errors.New("mink: encryption key revoked")
var ErrProviderClosed   = errors.New("mink: encryption provider closed")
```

### Built-in Providers

**Local Provider** (testing/development):
```go
import "go-mink.dev/encryption/local"

key := make([]byte, 32)
rand.Read(key)

provider := local.New(
    local.WithKey("master-1", key),
    local.WithKey("tenant-A", tenantAKey),
)
defer provider.Close()

// Runtime key management
provider.AddKey("new-key", newKey)
provider.RevokeKey("old-key") // Crypto-shredding
```

**AWS KMS Provider** (production):
```go
import "go-mink.dev/encryption/kms"

provider := kms.New(kms.WithKMSClient(kmsClient))
defer provider.Close()

// Uses kms.GenerateDataKey with AES_256 spec
// Master keys are AWS KMS key ARNs or aliases
```

**HashiCorp Vault Transit Provider** (production):
```go
import "go-mink.dev/encryption/vault"

provider := vault.New(vault.WithVaultClient(myVaultClient))
defer provider.Close()

// VaultClient is a minimal interface -- inject your own wrapper
// GenerateDataKey: random DEK locally, encrypted via Vault Transit
```

### Configuring Field-Level Encryption

```go
import (
    "go-mink.dev"
    "go-mink.dev/encryption"
    "go-mink.dev/encryption/local"
)

encConfig := mink.NewFieldEncryptionConfig(
    // Required: encryption provider
    mink.WithEncryptionProvider(provider),

    // Required: default master key ID
    mink.WithDefaultKeyID("master-1"),

    // Required: which fields to encrypt per event type
    // Supports dot-separated paths for nested fields (e.g., "address.street")
    mink.WithEncryptedFields("CustomerCreated", "email", "phone", "ssn"),
    mink.WithEncryptedFields("AddressUpdated", "address.street", "address.zip"),

    // Optional: per-tenant encryption keys
    mink.WithTenantKeyResolver(func(tenantID string) string {
        return "tenant-" + tenantID
    }),

    // Optional: crypto-shredding handler
    mink.WithDecryptionErrorHandler(func(err error, eventType string, metadata mink.Metadata) error {
        if errors.Is(err, encryption.ErrKeyRevoked) {
            // Return nil to skip decryption -- data stays encrypted
            return nil
        }
        return err
    }),
)

// Create event store with encryption
store := mink.New(adapter, mink.WithFieldEncryption(encConfig))
```

### How It Works

**Encrypt on save** (in `Append()` / `SaveAggregate()`):
1. `GenerateDataKey(ctx, keyID)` -- one provider call per event
2. AES-256-GCM encrypt each configured field with the DEK plaintext
3. Store encrypted DEK + field list in `Metadata.Custom`:
   - `$encrypted_fields` -- JSON array of encrypted field names
   - `$encryption_key_id` -- master key ID
   - `$encrypted_dek` -- base64-encoded encrypted DEK
   - `$encryption_algorithm` -- `AES-256-GCM`
4. Zero DEK plaintext after use

**Decrypt on load** (in `Load()` / `LoadAggregate()`):
1. Check `$encrypted_fields` in metadata (skip if absent)
2. `DecryptDataKey(ctx, keyID, encryptedDEK)` -- recover DEK plaintext
3. AES-256-GCM decrypt each field locally
4. Zero DEK plaintext after use

**Pipeline ordering**: Serialize -> Schema Stamp -> **Encrypt** -> Persist -> Load -> **Decrypt** -> Upcast -> Deserialize

### Metadata Helpers

```go
// Check if an event has encrypted fields
if mink.IsEncrypted(event.Metadata) {
    fields := mink.GetEncryptedFields(event.Metadata)  // []string{"email", "phone"}
    keyID := mink.GetEncryptionKeyID(event.Metadata)    // "master-1"
}
```

### Inspecting Raw Data

```go
// Load raw events without decryption
raw, _ := store.LoadRaw(ctx, "Customer-cust-1", 0)
fmt.Printf("Raw data at rest: %s\n", raw[0].Data)
// {"name":"Alice Smith","email":"base64-encrypted...","phone":"base64-encrypted..."}

fmt.Printf("Encrypted fields: %v\n", mink.GetEncryptedFields(raw[0].Metadata))
// [email phone]
```

### Zero Overhead

When `FieldEncryptionConfig` is not set (i.e., `mink.New(adapter)` without `WithFieldEncryption`), all encryption code paths are bypassed via nil-check. There is zero performance impact on applications that don't use encryption.

---

## GDPR Compliance

go-mink combines several features for compliance. **Crypto-shredding** (right to
erasure) and **data export** (right to access) are covered below, and
[**Audit Logging**](/docs/advanced/audit-logging) provides the queryable trail of
*who changed what, when* that many regimes require (e.g. GDPR Article 30).

:::tip Full guide
This page is the encryption + primitives reference. For the complete, task-oriented
workflow — subject discovery, one-call **`DataEraser`** (with sibling-store erasure,
blast-radius guard, and a verification certificate), retention, and the subject index —
see **[GDPR & Data Governance](/docs/security)**.
:::

### Crypto-Shredding

Make personal data permanently unrecoverable by revoking encryption keys. Since PII fields are encrypted with per-tenant keys, revoking a tenant's key makes all their encrypted data unreadable -- even though the events remain in the store.

```go
import (
    "go-mink.dev"
    "go-mink.dev/encryption"
    "go-mink.dev/encryption/local"
)

// 1. Set up per-tenant encryption keys
provider := local.New(
    local.WithKey("tenant-A", tenantAKey),
    local.WithKey("tenant-B", tenantBKey),
)

encConfig := mink.NewFieldEncryptionConfig(
    mink.WithEncryptionProvider(provider),
    mink.WithDefaultKeyID("tenant-A"),
    mink.WithEncryptedFields("CustomerCreated", "email", "phone"),
    mink.WithTenantKeyResolver(func(tenantID string) string {
        return "tenant-" + tenantID
    }),
    // Graceful degradation when key is revoked
    mink.WithDecryptionErrorHandler(func(err error, eventType string, metadata mink.Metadata) error {
        if errors.Is(err, encryption.ErrKeyRevoked) {
            fmt.Printf("Key revoked for tenant %s -- data shredded\n", metadata.TenantID)
            return nil // Return encrypted data as-is
        }
        return err
    }),
)

store := mink.New(adapter, mink.WithFieldEncryption(encConfig))

// 2. Normal operation -- data is encrypted/decrypted transparently
customer := NewCustomer("cust-1")
customer.Create("Alice", "alice@example.com", "+1-555-0100")
store.SaveAggregate(ctx, customer)

loaded := NewCustomer("cust-1")
store.LoadAggregate(ctx, loaded) // email/phone decrypted automatically

// 3. GDPR deletion request -- revoke tenant B's key
provider.RevokeKey("tenant-B")

// Tenant B's encrypted fields are now permanently unrecoverable
// Tenant A's data is still fully accessible
// Events remain in the store (audit trail preserved)
// Non-encrypted fields (name, country) are still readable
```

**Portable revocation.** Revocation is an *optional* provider capability
(`encryption.Revocable`). Call it via `encryption.Revoke(provider, keyID)` /
`encryption.IsRevoked(...)` rather than type-asserting — a provider that does not support
it returns `encryption.ErrRevocationUnsupported`. The built-in providers implement it
(local zeroes the key; **AWS KMS** schedules deletion — immediately unusable, destroyed
after the 7–30 day window; **Vault Transit** deletes the key), with KMS/Vault gaining it
through an optional client sub-interface so your injected client never changes.
`encryption.RecoverableRevocable` adds a **grace window** (`SoftRevoke`/`Unrevoke`) so an
accidental erasure can be undone before it becomes permanent, and
`encryption.GetRevocationState` reports `NotRevoked` / `SoftRevoked` / `Revoked`. See the
[GDPR guide](/docs/security#crypto-shredding-key-revocation) for the full erasure workflow.

### Data Export (Right to Access / Data Portability)

The `DataExporter` collects events belonging to a data subject and returns them in a portable format. It integrates with field-level encryption: when a key has been revoked (crypto-shredding), affected events are included with `Redacted=true` and `nil` Data.

```go
import "go-mink.dev"

exporter := mink.NewDataExporter(store,
    mink.WithExportBatchSize(500),  // Events per batch during scan
    mink.WithExportLogger(logger),
)

// Strategy 1: Stream-based -- when you know the stream IDs (efficient, no scan)
result, err := exporter.Export(ctx, mink.ExportRequest{
    SubjectID: "user-123",
    Streams:   []string{"Customer-user-123", "Order-ord-456"},
})

// Strategy 2: Scan-based -- filter all events (requires SubscriptionAdapter)
result, err := exporter.Export(ctx, mink.ExportRequest{
    SubjectID: "tenant-A-data",
    Filter:    mink.FilterByTenantID("A"),
})

// Strategy 3: Streaming -- memory-efficient for large exports
err := exporter.ExportStream(ctx, mink.ExportRequest{
    SubjectID: "user-123",
    Streams:   []string{"Customer-user-123"},
}, func(ctx context.Context, event mink.ExportedEvent) error {
    // Write to file, send via API, etc.
    if event.Redacted {
        // Encrypted data -- key was revoked
        return nil
    }
    return writeToJSON(event)
})
```

**ExportResult** contains:

| Field | Description |
|-------|-------------|
| `SubjectID` | The data subject identifier from the request |
| `Events` | All exported events (including redacted ones) |
| `Streams` | Unique stream IDs that contained matching events |
| `TotalEvents` | Total event count (including redacted) |
| `RedactedCount` | Events whose PII could not be decrypted |
| `ExportedAt` | Timestamp when the export was generated |

**Built-in filters** for scan-based export:

```go
mink.FilterByTenantID("tenant-A")                     // Match tenant ID
mink.FilterByUserID("user-123")                        // Match user ID
mink.FilterByStreamPrefix("Customer-")                 // Match stream prefix
mink.FilterByMetadata("department", "sales")           // Match custom metadata
mink.FilterByEventTypes("CustomerCreated", "OrderPlaced")  // Match event types

// Combine filters (AND logic)
mink.CombineFilters(
    mink.FilterByTenantID("A"),
    mink.FilterByEventTypes("OrderPlaced"),
)
```

**Time range filtering**:

```go
from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
to := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)

result, err := exporter.Export(ctx, mink.ExportRequest{
    SubjectID: "user-123",
    Streams:   []string{"Customer-user-123"},
    FromTime:  &from,
    ToTime:    &to,
})
```

**Crypto-shredding integration**: When a key has been revoked, the exporter catches the decryption error and marks the event as redacted. The `RawData` field still contains the original encrypted bytes, and all non-PII metadata (stream ID, event type, timestamp, version) remains available.

```go
// After revoking a key:
result, _ := exporter.Export(ctx, mink.ExportRequest{
    SubjectID: "user-123",
    Streams:   []string{"Customer-user-123"},
})

for _, e := range result.Events {
    if e.Redacted {
        fmt.Printf("Redacted: %s at %s\n", e.EventType, e.Timestamp)
    }
}
fmt.Printf("Total: %d, Redacted: %d\n", result.TotalEvents, result.RedactedCount)
```

### Data Erasure (Right to be Forgotten)

`DataEraser` is the erasure counterpart to `DataExporter`: in one call it resolves the
subject, revokes its encryption keys (crypto-shred), redacts read models, erases derived
PII in **sibling stores** (audit trail, saga state, snapshots, outbox, idempotency), runs
external-PII hooks, appends an optional marker, and emits a verification certificate.

```go
eraser := mink.NewDataEraser(store,
    mink.WithEraseSubjectResolver(resolver),
    mink.WithReadModelRedactor(usersReadModel),
    mink.WithSubjectStore(mink.NewAuditSubjectEraser(auditStore)), // reach derived PII
    mink.WithSharedKeyGuard(),                                     // refuse to nuke a shared per-tenant key
    mink.WithCertificateSink(writeToAuditStore),
)
res, _ := eraser.Erase(ctx, mink.ErasureRequest{SubjectID: "user-123"})
report, _ := eraser.Verify(ctx, "user-123") // is any recoverable PII left?
```

`Erase` is idempotent; partial failures are reported in `res.Errors` / `res.Failed()`,
never fatal. The full workflow — blast-radius guard, strict accountability, the discovery
race, and the subject index — is documented in the
[GDPR & Data Governance guide](/docs/security#data-erasure-article-17).

### Data Retention

`RetentionManager` enforces time-based retention **without deleting event rows** (the log
stays append-only). A `RetentionPolicy` is a matcher (`Category` / `StreamPrefix` /
`EventTypes` / `TenantID` / `MaxAge`) plus an action — `ActionShred` (revoke the key),
or `ActionRedactFields` / `ActionAnonymize` (applied to read models via the policy's
`Apply` hook, since go-mink cannot mutate event rows).

```go
mgr := mink.NewRetentionManager(store, []mink.RetentionPolicy{
    {Name: "old-customers", Category: "Customer", MaxAge: 365 * 24 * time.Hour, Action: mink.ActionShred},
})

report, _ := mgr.DryRun(ctx) // preview, no changes
report, _ = mgr.Apply(ctx)   // report.Matched, report.KeysRevoked, report.Skipped, report.Errors
```

`Apply` is a **single sweep** — schedule it yourself (cron/gocron). A `RedactFields` /
`Anonymize` policy with no `Apply` hook is surfaced loudly via `mgr.Validate()` /
`report.Failed()`, never silently skipped. See the
[Retention section](/docs/security#retention-policies) of the GDPR guide.

### Audit Logging

The command **audit trail** — an immutable record of *who ran what command, when, and
with what outcome* — has its own dedicated page: **[Audit Logging](/docs/advanced/audit-logging)**.
It ships as a command-bus middleware (`mink.AuditMiddleware`) backed by an `AuditStore`
(in-memory + PostgreSQL). For GDPR, a subject's audit rows can be **erased** via
`mink.NewAuditSubjectEraser` (registered on the `DataEraser` — see
[Sibling stores](/docs/security#sibling-stores--audit-saga-snapshots-outbox-idempotency)).

---

## Event Versioning & Upcasting

Handle schema evolution without breaking existing events. For comprehensive documentation, see the dedicated [Event Versioning](/docs/advanced/versioning) page.

### How It Works

Schema version is stored in `Metadata.Custom["$schema_version"]` -- no database migration needed. Events without a version are treated as version 1.

```go
// Define upcasters -- pure byte-level transformations
type orderCreatedV1ToV2 struct{}

func (u orderCreatedV1ToV2) EventType() string { return "OrderCreated" }
func (u orderCreatedV1ToV2) FromVersion() int  { return 1 }
func (u orderCreatedV1ToV2) ToVersion() int    { return 2 }
func (u orderCreatedV1ToV2) Upcast(data []byte, metadata mink.Metadata) ([]byte, error) {
    var m map[string]interface{}
    json.Unmarshal(data, &m)
    m["currency"] = "USD"
    return json.Marshal(m)
}

// Register with EventStore
chain := mink.NewUpcasterChain()
chain.Register(orderCreatedV1ToV2{})
chain.Validate() // check for gaps

store := mink.New(adapter, mink.WithUpcasters(chain))

// Old events are transparently upcasted during Load/LoadAggregate
// New events are stamped with $schema_version during Append/SaveAggregate
```

### Schema Compatibility Checking

```go
registry := mink.NewSchemaRegistry()
registry.Register("OrderCreated", mink.SchemaDefinition{
    Version: 1,
    Fields: []mink.FieldDefinition{
        {Name: "order_id", Type: "string", Required: true},
    },
})
registry.Register("OrderCreated", mink.SchemaDefinition{
    Version: 2,
    Fields: []mink.FieldDefinition{
        {Name: "order_id", Type: "string", Required: true},
        {Name: "currency", Type: "string", Required: false},
    },
})

compat, _ := registry.CheckCompatibility("OrderCreated", 1, 2)
// SchemaFullyCompatible | SchemaBackwardCompatible | SchemaForwardCompatible | SchemaBreaking
```

---

## Time-Travel Queries

Query state at any point in time.

```go
// Load aggregate at specific point in time
func (s *EventStore) LoadAggregateAt(ctx context.Context, agg Aggregate,
    timestamp time.Time) error {

    events, err := s.LoadStreamUntil(ctx, agg.AggregateID(), timestamp)
    if err != nil {
        return err
    }

    for _, event := range events {
        if err := agg.ApplyEvent(event.Data); err != nil {
            return err
        }
    }

    return nil
}

// Load at specific version
func (s *EventStore) LoadAggregateVersion(ctx context.Context, agg Aggregate,
    version int64) error {

    events, err := s.LoadStreamRange(ctx, agg.AggregateID(), 1, int(version))
    if err != nil {
        return err
    }

    for _, event := range events {
        agg.ApplyEvent(event.Data)
    }

    return nil
}

// Usage example: Debug a production issue
func debugOrderState(orderID string, beforeRefund time.Time) {
    order := NewOrder(orderID)

    // Load state just before the refund was processed
    store.LoadAggregateAt(ctx, order, beforeRefund.Add(-1*time.Second))

    fmt.Printf("Order state before refund:\n")
    fmt.Printf("  Status: %s\n", order.Status)
    fmt.Printf("  Total: %.2f\n", order.Total)
    fmt.Printf("  Items: %d\n", len(order.Items))
}
```

---

Next: [CLI →](/docs/guide/cli)

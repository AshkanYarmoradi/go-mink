---
layout: default
title: Security
nav_order: 9
permalink: /docs/security
---

# Security & Compliance
{: .no_toc }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

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
    Plaintext  []byte // 32-byte AES key — NEVER persisted, zeroed after use
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
import "github.com/AshkanYarmoradi/go-mink/encryption/local"

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
import "github.com/AshkanYarmoradi/go-mink/encryption/kms"

provider := kms.New(kms.WithKMSClient(kmsClient))
defer provider.Close()

// Uses kms.GenerateDataKey with AES_256 spec
// Master keys are AWS KMS key ARNs or aliases
```

**HashiCorp Vault Transit Provider** (production):
```go
import "github.com/AshkanYarmoradi/go-mink/encryption/vault"

provider := vault.New(vault.WithVaultClient(myVaultClient))
defer provider.Close()

// VaultClient is a minimal interface — inject your own wrapper
// GenerateDataKey: random DEK locally, encrypted via Vault Transit
```

### Configuring Field-Level Encryption

```go
import (
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/encryption"
    "github.com/AshkanYarmoradi/go-mink/encryption/local"
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
            // Return nil to skip decryption — data stays encrypted
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
1. `GenerateDataKey(ctx, keyID)` — one provider call per event
2. AES-256-GCM encrypt each configured field with the DEK plaintext
3. Store encrypted DEK + field list in `Metadata.Custom`:
   - `$encrypted_fields` — JSON array of encrypted field names
   - `$encryption_key_id` — master key ID
   - `$encrypted_dek` — base64-encoded encrypted DEK
   - `$encryption_algorithm` — `AES-256-GCM`
4. Zero DEK plaintext after use

**Decrypt on load** (in `Load()` / `LoadAggregate()`):
1. Check `$encrypted_fields` in metadata (skip if absent)
2. `DecryptDataKey(ctx, keyID, encryptedDEK)` — recover DEK plaintext
3. AES-256-GCM decrypt each field locally
4. Zero DEK plaintext after use

**Pipeline ordering**: Serialize → Schema Stamp → **Encrypt** → Persist → Load → **Decrypt** → Upcast → Deserialize

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

### Crypto-Shredding

Make personal data permanently unrecoverable by revoking encryption keys. Since PII fields are encrypted with per-tenant keys, revoking a tenant's key makes all their encrypted data unreadable — even though the events remain in the store.

```go
import (
    "github.com/AshkanYarmoradi/go-mink"
    "github.com/AshkanYarmoradi/go-mink/encryption"
    "github.com/AshkanYarmoradi/go-mink/encryption/local"
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
            fmt.Printf("Key revoked for tenant %s — data shredded\n", metadata.TenantID)
            return nil // Return encrypted data as-is
        }
        return err
    }),
)

store := mink.New(adapter, mink.WithFieldEncryption(encConfig))

// 2. Normal operation — data is encrypted/decrypted transparently
customer := NewCustomer("cust-1")
customer.Create("Alice", "alice@example.com", "+1-555-0100")
store.SaveAggregate(ctx, customer)

loaded := NewCustomer("cust-1")
store.LoadAggregate(ctx, loaded) // email/phone decrypted automatically

// 3. GDPR deletion request — revoke tenant B's key
provider.RevokeKey("tenant-B")

// Tenant B's encrypted fields are now permanently unrecoverable
// Tenant A's data is still fully accessible
// Events remain in the store (audit trail preserved)
// Non-encrypted fields (name, country) are still readable
```

### Data Export (Right to Access / Data Portability)

The `DataExporter` collects events belonging to a data subject and returns them in a portable format. It integrates with field-level encryption: when a key has been revoked (crypto-shredding), affected events are included with `Redacted=true` and `nil` Data.

```go
import "github.com/AshkanYarmoradi/go-mink"

exporter := mink.NewDataExporter(store,
    mink.WithExportBatchSize(500),  // Events per batch during scan
    mink.WithExportLogger(logger),
)

// Strategy 1: Stream-based — when you know the stream IDs (efficient, no scan)
result, err := exporter.Export(ctx, mink.ExportRequest{
    SubjectID: "user-123",
    Streams:   []string{"Customer-user-123", "Order-ord-456"},
})

// Strategy 2: Scan-based — filter all events (requires SubscriptionAdapter)
result, err := exporter.Export(ctx, mink.ExportRequest{
    SubjectID: "tenant-A-data",
    Filter:    mink.FilterByTenantID("A"),
})

// Strategy 3: Streaming — memory-efficient for large exports
err := exporter.ExportStream(ctx, mink.ExportRequest{
    SubjectID: "user-123",
    Streams:   []string{"Customer-user-123"},
}, func(ctx context.Context, event mink.ExportedEvent) error {
    // Write to file, send via API, etc.
    if event.Redacted {
        // Encrypted data — key was revoked
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

### Data Retention

```go
// RetentionPolicy defines how long to keep data
type RetentionPolicy struct {
    // Default retention for all events
    DefaultRetention time.Duration
    
    // Override per event type
    EventTypeRetention map[string]time.Duration
    
    // Override per category
    CategoryRetention map[string]time.Duration
    
    // Events to never delete (legal holds, etc.)
    ExemptEventTypes []string
}

// RetentionManager handles automatic deletion
type RetentionManager struct {
    store  *EventStore
    policy RetentionPolicy
}

func (m *RetentionManager) EnforceRetention(ctx context.Context) (*RetentionReport, error) {
    report := &RetentionReport{StartedAt: time.Now()}
    
    // Find events past retention
    for eventType, retention := range m.policy.EventTypeRetention {
        cutoff := time.Now().Add(-retention)
        
        expired, _ := m.store.QueryExpiredEvents(ctx, eventType, cutoff)
        for _, event := range expired {
            // Archive before deletion (optional)
            m.archiveEvent(ctx, event)
            
            // Delete from main store
            m.store.DeleteEvent(ctx, event.ID)
            report.DeletedCount++
        }
    }
    
    return report, nil
}

// CLI integration
// $ mink retention enforce --dry-run
// $ mink retention report
```

### Audit Logging

```go
// AuditLog tracks all data access
type AuditLog interface {
    LogAccess(ctx context.Context, entry AuditEntry) error
    Query(ctx context.Context, filter AuditFilter) ([]AuditEntry, error)
}

type AuditEntry struct {
    ID           string
    Timestamp    time.Time
    UserID       string
    Action       string // "read", "write", "delete", "export"
    ResourceType string // "event", "aggregate", "projection"
    ResourceID   string
    IPAddress    string
    UserAgent    string
    Success      bool
    ErrorMessage string
}

// Middleware for automatic audit logging
func AuditMiddleware(log AuditLog) Middleware {
    return func(next Handler) Handler {
        return func(ctx context.Context, cmd Command) error {
            entry := AuditEntry{
                ID:           uuid.NewString(),
                Timestamp:    time.Now(),
                UserID:       auth.UserFromContext(ctx),
                Action:       "write",
                ResourceType: "aggregate",
                ResourceID:   cmd.AggregateID(),
                IPAddress:    request.IPFromContext(ctx),
            }
            
            err := next(ctx, cmd)
            entry.Success = err == nil
            if err != nil {
                entry.ErrorMessage = err.Error()
            }
            
            log.LogAccess(ctx, entry)
            return err
        }
    }
}
```

---

## Event Versioning & Upcasting

Handle schema evolution without breaking existing events. For comprehensive documentation, see the dedicated [Event Versioning](versioning) page.

### How It Works

Schema version is stored in `Metadata.Custom["$schema_version"]` — no database migration needed. Events without a version are treated as version 1.

```go
// Define upcasters — pure byte-level transformations
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

Next: [CLI →](cli)

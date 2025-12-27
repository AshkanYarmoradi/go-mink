---
layout: default
title: Security
nav_order: 10
permalink: /docs/security
---

# Security & Compliance
{: .no_toc }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Event Encryption

Protect sensitive data in events with field-level encryption.

### Encryption Provider Interface

```go
// EncryptionProvider handles key management and encryption
type EncryptionProvider interface {
    // Encrypt data with key ID
    Encrypt(ctx context.Context, keyID string, plaintext []byte) ([]byte, error)
    
    // Decrypt data
    Decrypt(ctx context.Context, keyID string, ciphertext []byte) ([]byte, error)
    
    // Generate new data encryption key
    GenerateDataKey(ctx context.Context, keyID string) (*DataKey, error)
    
    // Rotate encryption key
    RotateKey(ctx context.Context, keyID string) error
}

type DataKey struct {
    KeyID      string
    Plaintext  []byte // For encrypting data
    Ciphertext []byte // For storage (encrypted by master key)
}

// Built-in providers
func NewAWSKMSProvider(client *kms.Client, masterKeyID string) EncryptionProvider
func NewVaultProvider(client *vault.Client, transitPath string) EncryptionProvider
func NewLocalProvider(masterKey []byte) EncryptionProvider // For testing only
```

### Field-Level Encryption

```go
// EncryptionConfig defines which fields to encrypt
type EncryptionConfig struct {
    Provider        EncryptionProvider
    KeyIDResolver   func(event Event) string // Per-tenant keys
    EncryptedFields map[string][]string      // eventType -> fields
}

// Configuration example
config := mink.EncryptionConfig{
    Provider: kms.NewProvider(awsConfig),
    KeyIDResolver: func(event Event) string {
        // Use tenant-specific key
        return fmt.Sprintf("alias/tenant-%s", event.Metadata.TenantID)
    },
    EncryptedFields: map[string][]string{
        "CustomerCreated": {"email", "phone", "ssn", "address"},
        "OrderCreated":    {"shippingAddress", "billingAddress"},
        "PaymentProcessed": {"cardLastFour", "cardToken"},
    },
}

store := mink.New(adapter, mink.WithEncryption(config))
```

### Encryption Middleware

```go
// Encryption serializer wraps standard serializer
type EncryptingSerializer struct {
    inner    Serializer
    provider EncryptionProvider
    config   EncryptionConfig
}

func (s *EncryptingSerializer) Serialize(event interface{}) ([]byte, error) {
    // Serialize to JSON first
    data, err := s.inner.Serialize(event)
    if err != nil {
        return nil, err
    }
    
    // Check if this event type has encrypted fields
    eventType := reflect.TypeOf(event).Name()
    fields, ok := s.config.EncryptedFields[eventType]
    if !ok {
        return data, nil
    }
    
    // Parse JSON, encrypt fields, re-serialize
    var doc map[string]interface{}
    json.Unmarshal(data, &doc)
    
    keyID := s.config.KeyIDResolver(event)
    for _, field := range fields {
        if val, ok := doc[field]; ok {
            plaintext, _ := json.Marshal(val)
            ciphertext, _ := s.provider.Encrypt(ctx, keyID, plaintext)
            doc[field] = base64.StdEncoding.EncodeToString(ciphertext)
            doc[field+"_encrypted"] = true
        }
    }
    
    return json.Marshal(doc)
}

func (s *EncryptingSerializer) Deserialize(data []byte, eventType string) (interface{}, error) {
    // Check for encrypted fields
    fields, hasEncrypted := s.config.EncryptedFields[eventType]
    if !hasEncrypted {
        return s.inner.Deserialize(data, eventType)
    }
    
    // Decrypt fields first
    var doc map[string]interface{}
    json.Unmarshal(data, &doc)
    
    for _, field := range fields {
        if doc[field+"_encrypted"] == true {
            ciphertext, _ := base64.StdEncoding.DecodeString(doc[field].(string))
            plaintext, _ := s.provider.Decrypt(ctx, keyID, ciphertext)
            var val interface{}
            json.Unmarshal(plaintext, &val)
            doc[field] = val
            delete(doc, field+"_encrypted")
        }
    }
    
    decrypted, _ := json.Marshal(doc)
    return s.inner.Deserialize(decrypted, eventType)
}
```

---

## GDPR Compliance

### Crypto-Shredding

Delete personal data by destroying encryption keys.

```go
// GDPRManager handles data subject rights
type GDPRManager interface {
    // Right to be forgotten - destroy encryption key
    ForgetDataSubject(ctx context.Context, subjectID string) error
    
    // Right to access - export all data
    ExportDataSubject(ctx context.Context, subjectID string) (*DataExport, error)
    
    // Right to rectification - note: events are immutable
    // Use compensating events instead
    CreateRectificationEvent(ctx context.Context, subjectID string, 
        corrections map[string]interface{}) error
}

type DataExport struct {
    SubjectID   string
    ExportedAt  time.Time
    Events      []ExportedEvent
    ReadModels  map[string]interface{}
}

// Implementation
type gdprManager struct {
    eventStore *EventStore
    keyStore   EncryptionProvider
}

func (m *gdprManager) ForgetDataSubject(ctx context.Context, subjectID string) error {
    // 1. Get the encryption key ID for this subject
    keyID := fmt.Sprintf("subject-%s", subjectID)
    
    // 2. Delete/disable the key - all encrypted data becomes unreadable
    if err := m.keyStore.DeleteKey(ctx, keyID); err != nil {
        return fmt.Errorf("failed to delete encryption key: %w", err)
    }
    
    // 3. Record the deletion (for audit)
    return m.eventStore.Append(ctx, "gdpr-audit", []interface{}{
        DataSubjectForgotten{
            SubjectID:   subjectID,
            ForgottenAt: time.Now(),
            KeyID:       keyID,
        },
    })
}

func (m *gdprManager) ExportDataSubject(ctx context.Context, subjectID string) (*DataExport, error) {
    // Find all events related to this subject
    events, _ := m.eventStore.QueryByMetadata(ctx, "subjectId", subjectID)
    
    export := &DataExport{
        SubjectID:  subjectID,
        ExportedAt: time.Now(),
    }
    
    for _, event := range events {
        export.Events = append(export.Events, ExportedEvent{
            Type:      event.Type,
            Timestamp: event.Timestamp,
            Data:      event.Data, // Decrypted automatically
        })
    }
    
    // Record the export (for audit)
    m.eventStore.Append(ctx, "gdpr-audit", []interface{}{
        DataSubjectExported{
            SubjectID:  subjectID,
            ExportedAt: time.Now(),
            EventCount: len(export.Events),
        },
    })
    
    return export, nil
}
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

Handle schema evolution without breaking existing events.

### Event Schema Versions

```go
// Versioned event with schema version
type VersionedEvent struct {
    Type          string
    SchemaVersion int
    Data          []byte
}

// Event with version annotation
type OrderCreatedV1 struct {
    OrderID    string `json:"orderId"`
    CustomerID string `json:"customerId"`
}

type OrderCreatedV2 struct {
    OrderID    string `json:"orderId"`
    CustomerID string `json:"customerId"`
    Currency   string `json:"currency"`   // New in V2
    Channel    string `json:"channel"`    // New in V2
}

// Current version alias
type OrderCreated = OrderCreatedV2
```

### Upcaster Interface

```go
// Upcaster transforms old event versions to current
type Upcaster interface {
    // Event type this upcaster handles
    EventType() string
    
    // Source version
    FromVersion() int
    
    // Target version  
    ToVersion() int
    
    // Transform event data
    Upcast(data []byte) ([]byte, error)
}

// Example upcaster
type OrderCreatedV1ToV2 struct{}

func (u *OrderCreatedV1ToV2) EventType() string  { return "OrderCreated" }
func (u *OrderCreatedV1ToV2) FromVersion() int   { return 1 }
func (u *OrderCreatedV1ToV2) ToVersion() int     { return 2 }

func (u *OrderCreatedV1ToV2) Upcast(data []byte) ([]byte, error) {
    var v1 OrderCreatedV1
    if err := json.Unmarshal(data, &v1); err != nil {
        return nil, err
    }
    
    v2 := OrderCreatedV2{
        OrderID:    v1.OrderID,
        CustomerID: v1.CustomerID,
        Currency:   "USD",      // Default for old events
        Channel:    "unknown",  // Default for old events
    }
    
    return json.Marshal(v2)
}
```

### Upcaster Chain

```go
// UpcasterChain applies upcasters in sequence
type UpcasterChain struct {
    upcasters map[string][]Upcaster // eventType -> upcasters (ordered by version)
}

func NewUpcasterChain() *UpcasterChain

func (c *UpcasterChain) Register(upcaster Upcaster) {
    eventType := upcaster.EventType()
    c.upcasters[eventType] = append(c.upcasters[eventType], upcaster)
    // Sort by FromVersion
    sort.Slice(c.upcasters[eventType], func(i, j int) bool {
        return c.upcasters[eventType][i].FromVersion() < c.upcasters[eventType][j].FromVersion()
    })
}

func (c *UpcasterChain) Upcast(eventType string, fromVersion int, data []byte) ([]byte, int, error) {
    upcasters := c.upcasters[eventType]
    currentVersion := fromVersion
    currentData := data
    
    for _, upcaster := range upcasters {
        if upcaster.FromVersion() == currentVersion {
            var err error
            currentData, err = upcaster.Upcast(currentData)
            if err != nil {
                return nil, 0, err
            }
            currentVersion = upcaster.ToVersion()
        }
    }
    
    return currentData, currentVersion, nil
}

// Integration with serializer
type UpcastingSerializer struct {
    inner   Serializer
    chain   *UpcasterChain
}

func (s *UpcastingSerializer) Deserialize(data []byte, eventType string, version int) (interface{}, error) {
    // Upcast to latest version
    upcastedData, _, err := s.chain.Upcast(eventType, version, data)
    if err != nil {
        return nil, err
    }
    
    return s.inner.Deserialize(upcastedData, eventType)
}
```

### Schema Registry

```go
// SchemaRegistry manages event schemas
type SchemaRegistry interface {
    // Register schema for event type
    Register(eventType string, version int, schema Schema) error
    
    // Get schema
    GetSchema(eventType string, version int) (Schema, error)
    
    // Get latest version
    GetLatestVersion(eventType string) (int, error)
    
    // Check compatibility between versions
    CheckCompatibility(eventType string, oldVersion, newVersion int) (Compatibility, error)
}

type Schema struct {
    Version     int
    JSONSchema  json.RawMessage
    Fields      []FieldDef
    CreatedAt   time.Time
}

type Compatibility int
const (
    FullyCompatible Compatibility = iota   // Both directions work
    BackwardCompatible                      // New can read old
    ForwardCompatible                       // Old can read new
    Breaking                                // Incompatible
)

// CLI commands
// $ mink schema register OrderCreated --version 2 --schema ./schemas/order_created_v2.json
// $ mink schema check OrderCreated --from 1 --to 2
// $ mink schema list OrderCreated
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

Next: [Testing →](testing)

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

provider := kms.New(
    kms.WithKMSClient(kmsClient),
    kms.WithPendingDeletionWindow(7),            // days (7–30) a revoked CMK stays recoverable
    kms.WithRevocationTimeout(30 * time.Second), // bounds the context-free revocation calls (default 30s)
)
defer provider.Close()

// Uses kms.GenerateDataKey with AES_256 spec.
// Master key ids are forwarded as KeyId and stamped into $encryption_key_id:
// stamp key ARNs (or bare key ids), NOT aliases — an alias is a mutable pointer
// that can be re-targeted after the event was written. The revocation methods
// resolve whatever id they are given to the canonical KeyMetadata.KeyId via
// DescribeKey AT CALL TIME (ScheduleKeyDeletion / CancelKeyDeletion reject aliases
// outright): RevokeKey("alias/x") shreds whatever the alias points at then, a
// re-pointed alias changes what IsRevoked answers, and a MISSING alias is
// reported as an unknown state (kms.ErrAliasNotFound), never as Revoked. With a client that also implements
// KMSRevocationClient and KMSDeletionCanceller (*kms.Client does), the provider
// implements encryption.Revocable, StatefulRevocable (a CMK pending deletion is
// SoftRevoked, only a completed deletion is Revoked) and RecoverableRevocable
// (SoftRevokeKey / UnrevokeKey). UnrevokeKey is state-driven and retryable —
// CancelKeyDeletion + EnableKey for a pending key, EnableKey alone for a key a
// half-finished restore left disabled — and returns *kms.KeyPermanentlyDeletedError
// (Is kms.ErrKeyPermanentlyDeleted and encryption.ErrKeyRevoked) once deleted.
```

**HashiCorp Vault Transit Provider** (production):
```go
import "go-mink.dev/encryption/vault"

provider := vault.New(vault.WithVaultClient(myVaultClient))
defer provider.Close()

// VaultClient is a minimal interface -- inject your own wrapper
// GenerateDataKey: random DEK locally, encrypted via Vault Transit
// Transit key names are validated BEFORE your client sees them — non-empty, no
// "/", no control bytes (< 0x20, 0x7f), not the whole segment "." or ".." — and
// handed over url.PathEscape'd ("tenant 42" arrives as "tenant%2042"; a client
// built on an SDK that escapes path segments itself must url.PathUnescape first).
// Everything else (spaces, '?', '%', non-ASCII, an inner "a..b") is accepted, so
// existing key names keep decrypting, while a key id derived from untrusted input
// (a tenant or subject id) can never escape the Transit mount or address another
// Vault API path. RevokeKey / IsRevoked are bounded by vault.WithRevocationTimeout
// (default vault.DefaultRevocationTimeout, 30s). An unsafe name is rejected
// with an encryption.EncryptionError and never forwarded.
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
    // Optional: per-subject keys (alias of WithTenantKeyResolver). With a
    // SubjectTagger configured, an event whose Metadata.TenantID is empty — every
    // event persisted via SaveAggregate — keys off its first $subjects tag, so
    // aggregate PII is individually crypto-shreddable. Prefer this name for
    // per-subject/GDPR setups; keep WithTenantKeyResolver for per-tenant.
    // mink.WithSubjectKeyResolver(func(subjectID string) string {
    //     return "subject-" + subjectID
    // }),
    // Optional: with a resolver configured, FAIL an append whose event offers no
    // tenant id / subject tag (or whose resolver returns "") instead of silently
    // wrapping it under the default key — an EncryptionError whose cause is a
    // *mink.KeyResolutionError (errors.Is: ErrEncryptionFailed, ErrKeyResolutionFailed).
    // mink.WithRequireKeyResolution(),

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
if err := encConfig.Validate(); err != nil { // malformed field path (ErrInvalidEncryptedFieldPath) — surface at startup
    log.Fatal(err)
}
store := mink.New(adapter, mink.WithFieldEncryption(encConfig))
```

Field paths may overlap: listing both a parent and one of its nested fields (`"address"` and
`"address.street"`, in either order) is valid — fields are sealed deepest-first and unsealed
shallowest-first, so they always round-trip, and duplicates are collapsed. A configured path
whose parent is present but **not a JSON object** (a string, number, array or bool) fails the
append with an `EncryptionError` naming the field rather than silently storing plaintext; an
absent or `null` parent, like an absent leaf, is optional and simply not encrypted. Paths with
an empty segment (`""`, `"a..b"`, `".a"`, `"a."`) are rejected by `Validate()` and by the
first append of the affected event type.

:::tip Per-subject keys for aggregate PII (GDPR)
Which master key wraps each event is chosen by precedence: **(1)** `Metadata.TenantID`,
**(2)** the first `$subjects` tag, **(3)** the default key. Events persisted via
`SaveAggregate` carry an empty `Metadata{}` (no `TenantID`), so with a
[`WithSubjectTagger`](/docs/security) configured they key off their subject tag — the
**same** tagger that defines a subject's erasure footprint also selects its shred key,
so the two can never drift. Pair it with `WithSubjectKeyResolver` (a legibility alias of
`WithTenantKeyResolver`) to map each subject to its own key, making that subject's PII
individually crypto-shreddable. This is the mechanism that lets you *avoid* shared keys;
[`WithSharedKeyGuard`](/docs/security) is the complementary backstop that *detects* a key
still shared across subjects before an erasure revokes it. Decryption is unaffected — the
wrapping key id is read from each event's own metadata, so events written under either
rule decrypt with no migration.
:::

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

:::note Security notes
- **The envelope is library-owned.** `Append` / `SaveAggregate` (and `EventStoreWithOutbox`)
  strip any caller-supplied `$encrypted_fields`, `$encryption_key_id`, `$encrypted_dek` and
  `$encryption_algorithm` from `Metadata.Custom` *before* stamping — exported as
  `mink.SanitizeReservedMetadata(m)` (copy-on-write, never mutates your map) — so metadata
  copied from an untrusted writer cannot make plaintext look encrypted or redirect decryption
  and erasure to another key. The same pass **drops** a caller-supplied `$schema_version`
  (the upcaster chain's latest version is stamped; `mink.WithCallerSchemaVersion()` lets
  trusted migration tooling keep an in-range value for a type that has upcasters) and lets a
  configured `SubjectTagger` **replace** caller `$subjects` tags (`mink.WithCallerSubjectTags()`
  restores merging) — a policy that also governs `EncryptStoredEvent` /
  `ReEncryptStreamInPlace` and the copies `ReEncryptStream` makes, so a tag forged *at rest*
  cannot select the wrapping key either. Other `$`-keys are stored as supplied.
- **Decryption fails closed.** A field listed in `$encrypted_fields` that is absent, not a
  string, or whose parent is missing returns `encryption.ErrDecryptionFailed` (an
  `EncryptionError` naming the field and key) instead of being silently skipped — a tampered
  event can no longer pass a substituted value through as plaintext.
- **Numbers round-trip exactly.** Encrypted events are decoded with `UseNumber`, so `int64`
  / `uint64` values above 2^53 in *any* field of an encrypted event are preserved
  byte-for-byte rather than rounded through `float64`.
- **Local provider key material** is never handed out by reference: each operation works on
  a private copy that is zeroed afterwards.
:::

### Metadata Helpers

```go
// Check if an event has encrypted fields (the READ-path signal: $encrypted_fields present,
// so a damaged envelope is still routed through decryption and fails loudly)
if mink.IsEncrypted(event.Metadata) {
    fields := mink.GetEncryptedFields(event.Metadata)  // []string{"email", "phone"}
    keyID := mink.GetEncryptionKeyID(event.Metadata)    // "master-1"
}

// Before ACTING on a key id (crypto-shredding, blast-radius guards, erasure planning)
// ask the stronger question: is the envelope complete — fields + key id + wrapped DEK?
// Only such an event is ciphertext that revoking its key would erase; a bare key id
// protects nothing and must never cause a revocation. This is the predicate DataEraser,
// SubjectResolver, Verify and RetentionManager all share.
if mink.HasEncryptionEnvelope(event.Metadata) {
    // safe to count event.Metadata's key id toward a revocation
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

Make personal data unrecoverable by revoking encryption keys. Since PII fields are encrypted with per-tenant keys, revoking a tenant's key makes all their encrypted data unreadable -- even though the events remain in the store. The erasure is final only once the provider has destroyed the key material: immediately for `local`, but only after the pending-deletion window for AWS KMS (see *Portable revocation* below). Fields that are not in `WithEncryptedFields` are untouched.

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

// Tenant B's encrypted fields are now unrecoverable (with AWS KMS: once the pending-deletion window elapses)
// Tenant A's data is still fully accessible
// Events remain in the store (audit trail preserved)
// Non-encrypted fields (name, country) are still readable
```

**Portable revocation.** Revocation is an *optional* provider capability
(`encryption.Revocable`). Call it via `encryption.Revoke(provider, keyID)` /
`encryption.IsRevoked(...)` rather than type-asserting — a provider that does not support
it returns `encryption.ErrRevocationUnsupported`. The built-in providers implement it
(local zeroes the key; **AWS KMS** schedules deletion — immediately unusable, but
`CancelKeyDeletion` can restore it until the 7–30 day pending window elapses, so the
provider's `RevocationState` reports `SoftRevoked` (still recoverable) until AWS completes
the deletion and no erasure is certified inside the window; **Vault Transit** deletes the
key), with KMS/Vault gaining it
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

// Strategy 1: Stream-based -- when you know the stream IDs (efficient, no scan).
// With Streams and a nil Filter, SubjectOrUntaggedFilter(SubjectID) is applied:
// events tagged ($subjects) for OTHER subjects are dropped, untagged ones kept.
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
mink.FilterByStreamPrefix("Customer-")                 // Match stream prefix ("user-1" also matches "user-10")
mink.FilterByStreams("Customer-user-123", "Order-ord-1") // Match exact stream IDs
mink.FilterByStreamCategory("Customer")                // Match the category (text before the first '-')
mink.FilterByMetadata("department", "sales")           // Match custom metadata
mink.FilterByEventTypes("CustomerCreated", "OrderPlaced")  // Match event types
mink.SubjectOrUntaggedFilter("user-123")               // Tagged for the subject, or untagged (the Streams default)

// Combine filters (AND logic)
mink.CombineFilters(
    mink.FilterByTenantID("A"),
    mink.FilterByEventTypes("OrderPlaced"),
)
```

Filters **fail closed**: an empty selector matches *nothing* (`FilterByTenantID("")`,
`FilterByUserID("")`, `FilterByStreamPrefix("")`, `FilterByMetadata` with an empty key or
value), `CombineFilters()` with no filters matches nothing, and a `nil` filter inside
`CombineFilters` matches nothing rather than panicking — an empty selector is never a
wildcard in a GDPR export.

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
// res.KeysRevoked / res.KeysFailed / res.CleartextEvents / res.Notes / res.Failed()
report, _ := eraser.Verify(ctx, "user-123") // is any recoverable PII left — in events AND
// in the registered sibling stores (report.ResidualStores / report.UncheckedStores)?
```

`Erase` is idempotent; partial failures are reported in `res.Errors` / `res.Failed()`,
never fatal. The built-in sibling-store erasers purge by the subject's resolved footprint
(`mink.SubjectFootprintIDs`: subject id + the footprint streams **exclusive** to the subject;
streams shared with other subjects are skipped and reported as `SharedStreamsSkipped`, and
derived aggregate ids are reached only with `mink.WithDerivedAggregateIDs()` /
`SubjectFootprintIDsWithDerived` — safe only for globally unique aggregate ids) and can count
what remains (`SubjectResidualCounter`); the certificate is never *vacuously* `Verified`
(zero checked events with keys revoked yields `Verified=false` plus a note, and
`Verify` flags such a report as `Vacuous`); every path decides "is this ciphertext?" with
`mink.HasEncryptionEnvelope`; a `SharedKeyError` message carries counts only (never other
subjects' ids, never key ids); and `WithSubjectIndexPurge` removes the subject from a
subject index only after a verified erasure (verified internally when no certificate sink is
configured). The full workflow — blast-radius
guard, strict accountability, the discovery race, and the subject index — is documented in
the [GDPR & Data Governance guide](/docs/security#data-erasure-article-17).

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

report, _ := mgr.DryRun(ctx) // preview: report.KeysToRevoke, report.SharedKeysSkipped, report.UnencryptedMatches
report, _ = mgr.Apply(ctx)   // report.Matched, report.KeysRevoked, report.Skipped, report.Errors
```

`Apply` is a **single sweep** — schedule it yourself (cron/gocron); for a large store, add
`WithRetentionCheckpoint(checkpointStore, name)` so the sweep resumes from a persisted
frontier instead of re-scanning the whole log each run (cost then tracks the retention
window, not total history), plus `WithRetentionMaxScan(n)` to bound a single run. Both are
opt-in — unset, `Apply` scans from position 0 as before. A `RedactFields` /
`Anonymize` policy with no `Apply` hook is surfaced loudly via `mgr.Validate()` /
`report.Failed()`, never silently skipped.

A shared-key **blast-radius guard is on by default**: `ActionShred` revokes a key only when
*every* event encrypted under it is covered by a Shred policy of the sweep; a key that also
protects out-of-scope events is listed in `report.SharedKeysSkipped` with a
`*RetentionSharedKeyError` (`mink.ErrRetentionSharedKey`), and
`mink.WithAllowSharedKeyRevocation()` opts out. A refused (or failed) Shred match counts in
`report.Skipped`, not `Acted`, and with a checkpoint it is re-swept on every later run until
its key is revoked. Matched events with no encryption envelope (`mink.HasEncryptionEnvelope`)
are counted in `report.UnencryptedMatches` (`mink.ErrRetentionUnencryptedMatches`), a policy
with no matchers at all is rejected with `mink.ErrRetentionUnscopedPolicy`, and `DryRun`
previews `KeysToRevoke` / `SharedKeysSkipped` without revoking anything. See the
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

Schema version is stored in `Metadata.Custom["$schema_version"]` -- no database migration needed. Events without a version are treated as version 1. A caller-supplied `$schema_version` is dropped on `Append` by default — the chain's latest version is stamped (nothing when no chain is configured) — so untrusted metadata cannot mis-route the upcaster chain; `mink.WithCallerSchemaVersion()` lets trusted migration tooling keep a value within `[1, latest]` for a type that has upcasters. A panicking upcaster is recovered and surfaced as an `*UpcastError` (`ErrUpcastFailed`) naming the event type and version transition.

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

# Field-Level Encryption Example

> Encrypt PII at rest, decrypt transparently on load, and crypto-shred per tenant for GDPR erasure.

Event stores keep every change forever, so any PII written today lives in your history indefinitely — that is a compliance liability unless it is protected. go-mink encrypts individual event fields (email, phone) with envelope encryption while leaving other fields (name, country) as plaintext so they stay queryable. Because each tenant has its own key, revoking a key permanently destroys that tenant's data — satisfying the GDPR "right to erasure" without rewriting immutable events.

## What this demonstrates
- **Field-level encryption** — `WithEncryptedFields("CustomerCreated", "email", "phone")` encrypts only the named fields; `name` and `country` remain plaintext and queryable.
- **Transparent decryption** — `LoadAggregate` / `Load` decrypt automatically, so domain code never sees ciphertext.
- **Per-tenant keys** — `WithTenantKeyResolver` maps each event's `Metadata.TenantID` to a key ID, isolating tenants cryptographically.
- **Crypto-shredding** — `provider.RevokeKey("tenant-B")` makes that tenant's data permanently unrecoverable (GDPR right to erasure), while other tenants stay readable.
- **Graceful degradation** — `WithDecryptionErrorHandler` catches `encryption.ErrKeyRevoked` and returns the still-encrypted payload instead of failing the load.

## Running
```bash
go run ./examples/encryption
```
No infrastructure required — encryption keys and the event store are in-memory (`local.New` + `memory.NewAdapter`).

## What happens
1. Two random 32-byte keys are generated and registered with a `local` provider as `tenant-A` and `tenant-B`. A `FieldEncryptionConfig` marks `email` and `phone` on `CustomerCreated` as encrypted.
2. **Encrypt on save, decrypt on load** — a customer is saved with the tenant-A key. `LoadRaw` prints the ciphertext at rest plus the encrypted-field list and key ID from metadata, then `LoadAggregate` prints the fully decrypted name, email, and phone.
3. **Per-tenant keys** — a second customer is appended with `Metadata{TenantID: "B"}`. The resolver picks `tenant-B`; the printed key ID confirms it, and `Load` returns the decrypted email.
4. **Crypto-shredding** — `provider.RevokeKey("tenant-B")` is called. Reloading tenant B's event triggers the decryption-error handler, which prints `[SHREDDED]` and leaves the email encrypted.
5. **Isolation** — tenant A's customer is loaded one last time and still decrypts normally, proving revocation only affected tenant B.

## Key APIs
- `mink.NewFieldEncryptionConfig(...)` — build the encryption config from options.
- `mink.WithEncryptionProvider(provider)` — supply the key provider that performs envelope encryption.
- `mink.WithDefaultKeyID("tenant-A")` — key used when no tenant-specific key is resolved.
- `mink.WithEncryptedFields(eventType, fields...)` — declare which fields on an event type are encrypted.
- `mink.WithTenantKeyResolver(func(tenantID) string)` — derive a key ID from each event's tenant.
- `mink.WithDecryptionErrorHandler(func(err, eventType, metadata) error)` — decide what happens when decryption fails (e.g. after key revocation).
- `mink.WithFieldEncryption(encConfig)` — attach the config to the event store via `mink.New`.
- `mink.GetEncryptedFields(metadata)` / `mink.GetEncryptionKeyID(metadata)` — read encryption metadata off a stored event.
- `local.New(local.WithKey(...))` — in-memory AES-256-GCM provider; returns `(*Provider, error)`.
- `provider.RevokeKey(keyID)` — crypto-shred: destroy a key so its data can no longer be decrypted.

## Related
- **Examples:** [export](../export) · [full-ecommerce](../full-ecommerce)
- **Docs:** [Security](https://go-mink.dev/docs/advanced/security) · [API reference](https://pkg.go.dev/go-mink.dev)

# Field-Level Encryption Example

> Encrypt PII at rest, decrypt transparently on load, and crypto-shred per tenant for GDPR erasure.

Event stores keep every change forever, so any PII written today lives in your history indefinitely — that is a compliance liability unless it is protected. go-mink encrypts individual event fields (email, phone) with envelope encryption while leaving other fields (name, country) as plaintext so they stay queryable. Because each tenant has its own key, revoking a key makes that tenant's *encrypted fields* unrecoverable — satisfying the GDPR "right to erasure" for those fields without rewriting immutable events.

> **Development provider.** This example uses `encryption/local`, an in-memory AES-256-GCM provider meant for development and tests: keys are generated at startup, live in process memory, and are gone when the process exits — no HSM, no key-usage audit trail, no rotation, no durable revocation record. Production deployments inject [`encryption/kms`](https://pkg.go.dev/go-mink.dev/encryption/kms) (AWS KMS) or [`encryption/vault`](https://pkg.go.dev/go-mink.dev/encryption/vault) (Vault Transit); the rest of the configuration is unchanged because every provider implements `encryption.Provider`.

## What this demonstrates
- **Field-level encryption** — `WithEncryptedFields("CustomerCreated", "email", "phone")` encrypts only the named fields; `name` and `country` remain plaintext and queryable.
- **Transparent decryption** — `LoadAggregate` / `Load` decrypt automatically, so domain code never sees ciphertext.
- **Per-tenant keys** — `WithTenantKeyResolver` maps each event's `Metadata.TenantID` to a key ID, isolating tenants cryptographically.
- **Crypto-shredding** — `provider.RevokeKey("tenant-B")` makes that tenant's *encrypted* fields unrecoverable (GDPR right to erasure), while other tenants stay readable.
- **What shredding does not cover** — after revocation the demo prints `name` and `country` in the clear: fields outside `WithEncryptedFields` are never erased by key revocation.
- **Graceful degradation** — `WithDecryptionErrorHandler` catches `encryption.ErrKeyRevoked` and returns the still-encrypted payload instead of failing the load.

## Running
```bash
go run ./examples/encryption
```
No infrastructure required — encryption keys and the event store are in-memory (`local.New` + `memory.NewAdapter`), which is exactly why this setup is dev-only (see the note above).

## What happens
1. Two random 32-byte keys are generated and registered with a `local` provider as `tenant-A` and `tenant-B`. A `FieldEncryptionConfig` marks `email` and `phone` on `CustomerCreated` as encrypted.
2. **Encrypt on save, decrypt on load** — a customer is saved with the tenant-A key. `LoadRaw` prints the ciphertext at rest (with `name`, `country` and `customer_id` visibly in plaintext) plus the encrypted-field list and key ID from metadata, then `LoadAggregate` prints the fully decrypted name, email, and phone.
3. **Per-tenant keys** — a second customer is appended with `Metadata{TenantID: "B"}`. The resolver picks `tenant-B`; the printed key ID confirms it, and `Load` returns the decrypted email.
4. **Crypto-shredding** — `provider.RevokeKey("tenant-B")` is called. Reloading tenant B's event triggers the decryption-error handler, which prints `[SHREDDED]`; the output then lists each field with whether it is ciphertext (email, phone — unrecoverable) or plaintext (name, country — **not erased**), and shows how to add `name` to the encrypted-field list.
5. **Isolation** — tenant A's customer is loaded one last time and still decrypts normally, proving revocation only affected tenant B.

## Caveats
- **Only configured fields are shredded.** Key revocation erases the fields in `WithEncryptedFields` and nothing else. Put every PII field on the list, or keep PII out of plaintext fields; the subject's *identifier* (`customer_id`, the stream ID, `$subjects` tags) is plaintext by design and is not erased either.
- **"Unrecoverable" depends on the provider.** `local` zeroes the key immediately. AWS KMS only *schedules* deletion: the key is unusable at once but can be restored with `CancelKeyDeletion` during the 7–30 day pending window, so an erasure is only final once that window has elapsed.
- **Per-tenant keys have a tenant-wide blast radius.** Revoking `tenant-B` shreds every subject under tenant B. For per-subject erasure use `WithSubjectKeyResolver` with one key per subject (see the [security guide](https://go-mink.dev/docs/security)).

## Key APIs
- `mink.NewFieldEncryptionConfig(...)` — build the encryption config from options.
- `mink.WithEncryptionProvider(provider)` — supply the key provider that performs envelope encryption.
- `mink.WithDefaultKeyID("tenant-A")` — key used when no tenant-specific key is resolved.
- `mink.WithEncryptedFields(eventType, fields...)` — declare which fields on an event type are encrypted (and therefore shreddable).
- `mink.WithTenantKeyResolver(func(tenantID) string)` — derive a key ID from each event's tenant.
- `mink.WithDecryptionErrorHandler(func(err, eventType, metadata) error)` — decide what happens when decryption fails (e.g. after key revocation).
- `mink.WithFieldEncryption(encConfig)` — attach the config to the event store via `mink.New`.
- `mink.GetEncryptedFields(metadata)` / `mink.GetEncryptionKeyID(metadata)` — read encryption metadata off a stored event.
- `local.New(local.WithKey(...))` — in-memory AES-256-GCM provider for development/tests; returns `(*Provider, error)`.
- `provider.RevokeKey(keyID)` — crypto-shred: destroy a key so its encrypted fields can no longer be decrypted.

## Related
- **Examples:** [export](../export) · [full-ecommerce](../full-ecommerce)
- **Docs:** [Security](https://go-mink.dev/docs/advanced/security) · [GDPR & Data Governance](https://go-mink.dev/docs/security) · [API reference](https://pkg.go.dev/go-mink.dev)

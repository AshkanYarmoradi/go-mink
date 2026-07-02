## 1. Required — Subject-scoped field keys (`subject-scoped-field-keys`)

- [ ] 1.1 Extend `FieldEncryptionConfig.resolveKeyID` (encryption.go): when `metadata.TenantID == ""`, fall back to the first `$subjects` tag (`GetSubjectTags`) as the resolver input. Precedence: `TenantID` → first subject tag → `defaultKeyID`. No change when `tenantKeyResolver` is nil.
- [ ] 1.2 Confirm the `$subjects` tag is present at `resolveKeyID` time on the `SaveAggregate` path — the tagger runs in `prepareEventData` before `encryptFields` (add a guard test rather than assume).
- [ ] 1.3 Table-driven tests:
  - `SaveAggregate` event (empty metadata) + `SubjectTagger` + `WithEncryptedFields` ⇒ the event's stored wrapping key id equals the subject's key (assert via `GetEncryptionKeyID`), and `RevokeKey(subjectKey)` makes the event decrypt-fail / export as `Redacted`.
  - Two subjects ⇒ two distinct key ids; revoking one leaves the other decryptable.
  - Explicit `TenantID` still wins over a present subject tag.
  - No tagger + no `TenantID` ⇒ `defaultKeyID` (unchanged).
  - No resolver ⇒ `defaultKeyID` (zero overhead / unchanged).
  - Events written under the tenant rule and under the subject rule both decrypt (key id read from metadata, D3).
- [ ] 1.4 Docs: on `WithEncryptedFields` / `WithTenantKeyResolver`, note that with a `SubjectTagger` configured, empty-`TenantID` (aggregate) events key off the first `$subjects` tag — one tagger drives both erasure footprint and shred key. Cross-link `WithSharedKeyGuard`.

## 2. Good-to-have — Subject key-resolver alias (`subject-key-resolver-alias`)

- [ ] 2.1 Add `WithSubjectKeyResolver(func(subjectID string) string) EncryptionOption` that sets the same `tenantKeyResolver` field.
- [ ] 2.2 Test: `WithSubjectKeyResolver` and `WithTenantKeyResolver` are interchangeable (same resolution outcomes).
- [ ] 2.3 Docs: recommend `WithSubjectKeyResolver` for per-subject/GDPR setups; keep `WithTenantKeyResolver` for per-tenant.

## 3. CHANGELOG
- [ ] 3.1 Add an entry under the field-encryption / GDPR section (new fallback + alias, backward compatible).

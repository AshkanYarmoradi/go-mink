## ADDED Requirements

### Requirement: Export never emits ciphertext for an undecryptable event
`DataExporter` SHALL independently detect an event whose encryption metadata is present
and whose fields remain encrypted — e.g. because a configured `WithDecryptionErrorHandler`
returned nil for a revoked/missing key — and mark it `Redacted=true` with `nil` `Data`,
regardless of the handler's decision. `RawData` retains only the unrecoverable
ciphertext (consistent with the existing key-revocation redaction), never a decrypted
or re-serialized payload. A crypto-shredded event SHALL NOT be exported as a
non-redacted record.

#### Scenario: Shredded event with a swallowing decryption handler is redacted
- **WHEN** the store is configured with a `WithDecryptionErrorHandler` that returns nil, a subject's key is revoked, and the subject is exported
- **THEN** the exported event has `Redacted=true` and `Data==nil` (no decrypted payload leaks); `RawData` holds only the unrecoverable ciphertext

#### Scenario: Normal decryptable event is unaffected
- **WHEN** an event decrypts successfully
- **THEN** it is exported with `Redacted=false` and its plaintext `Data` as before

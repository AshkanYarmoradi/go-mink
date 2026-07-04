# encryption-at-rest-e2e Specification

## Purpose
TBD - created by archiving change end-to-end-test-coverage. Update Purpose after archive.
## Requirements
### Requirement: Field encryption is verified ciphertext-at-rest on PostgreSQL
There SHALL be an end-to-end test that saves an aggregate with configured encrypted fields to a PostgreSQL store, reads the raw `data` column directly from the database and asserts the encrypted field values are ciphertext (the plaintext does not appear), and then `LoadAggregate`/`Load` transparently decrypts them. The encryption metadata MUST live in `Metadata.Custom` (no DB-schema change), and an unencrypted store MUST incur zero overhead. The suite SHALL be provider-parameterized and run against `encryption/local` by default.

#### Scenario: Encrypted field is ciphertext in storage, plaintext on load
- **WHEN** an event with an encrypted field is appended to PostgreSQL
- **THEN** a direct SQL read of the row's `data` shows the field as ciphertext (no plaintext), and `LoadAggregate` returns the decrypted plaintext value

#### Scenario: Zero overhead when encryption is not configured
- **WHEN** the same flow runs on a store without field encryption
- **THEN** events are stored and loaded unchanged with no encryption metadata added

### Requirement: KMS/Vault encryption and revocation are verified through the store (good-to-have, env-gated)
There SHALL be an end-to-end test that runs the ciphertext-at-rest flow with the AWS KMS provider (`AWS_*` + `MINK_KMS_TEST_KEY_ID`) and the Vault provider (`VAULT_ADDR`/`VAULT_TOKEN` + `MINK_VAULT_TEST_KEY`), and that revokes the key through `DataEraser`/`Revocable` and asserts a subsequent `Load` cannot decrypt (crypto-shred). Each MUST self-skip when its credentials are absent.

#### Scenario: Revoke-through-erasure makes stored data unrecoverable
- **WHEN** events are encrypted via a real KMS/Vault key, persisted to PostgreSQL, and the key is revoked through the erasure path
- **THEN** a subsequent `Load` of those events returns a decryption failure / redacted data, proving the shred; **WHEN** the provider credentials are unset the test skips


# key-lifecycle Specification

## Purpose
TBD - created by archiving change gdpr-erasure-and-retention. Update Purpose after archive.
## Requirements
### Requirement: Master-key rotation
The encryption configuration SHALL support master-key rotation in which the provider re-wraps data keys under a new master key without rewriting event rows; rotation SHALL be transparent to decrypt — events wrapped under the previous master keep decrypting until that master is retired.

#### Scenario: Decrypt works across a rotation
- **WHEN** the master key is rotated
- **THEN** events wrapped under the previous master still decrypt until that master is retired

### Requirement: Recoverable (grace-window) revocation
Revocation SHALL optionally support a configurable grace window — a soft-revoke that blocks decryption but can be undone within the window before becoming a permanent crypto-shred.

#### Scenario: Undo within the window
- **WHEN** a key is soft-revoked and then un-revoked within the grace window
- **THEN** decryption is restored and no permanent loss occurs

#### Scenario: Permanent after the window
- **WHEN** the grace window elapses for a soft-revoked key
- **THEN** the key becomes permanently unrecoverable (crypto-shredded)

### Requirement: Optional re-encryption sweep
The package SHALL provide an optional re-encryption sweep that re-encrypts targeted fields under fresh data keys (e.g. after a suspected key compromise) and MUST do so without mutating or deleting existing event rows.

#### Scenario: Re-encrypt without mutation
- **WHEN** a re-encryption sweep runs over targeted events
- **THEN** the fields are re-encrypted under new keys and no historical row is deleted or rewritten

### Requirement: ReEncryptStream is idempotent and does not imply erasure
`ReEncryptStream` SHALL append to the destination stream with an expected version of
`NoStream` so a re-run cannot silently duplicate the copy, SHALL strip stale
`$encryption_*` markers from carried metadata before re-appending (so a changed
field-encryption config leaves no stale markers), and SHALL return the copied count and
the source's old key ids. Its documentation SHALL state plainly that the source stream
and its old-key-recoverable PII survive until the caller retires the source and revokes
the old key(s) — the function alone erases nothing.

#### Scenario: Re-running does not duplicate
- **WHEN** `ReEncryptStream` is called a second time for the same destination
- **THEN** it fails the version guard rather than appending a duplicate copy

#### Scenario: Old key ids are returned for follow-up revocation
- **WHEN** `ReEncryptStream` completes
- **THEN** it returns the distinct old key ids so the caller can revoke them to remove the source's recoverable PII


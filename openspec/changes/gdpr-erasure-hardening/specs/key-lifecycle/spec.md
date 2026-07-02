## ADDED Requirements

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

## ADDED Requirements

### Requirement: Reconciliation honours the shared-key guard
`reconcileAfterRevoke` SHALL apply the same shared-key detection as the initial revoke
path to any newly-appeared ("newcomer") key when `WithSharedKeyGuard()` is set and
`AllowSharedKeyRevocation()` is not. A newcomer key that also protects another subject's
events SHALL NOT be revoked silently; the erasure SHALL surface a `SharedKeyError` and
set `ErasureResult.Partial`.

#### Scenario: A shared tenant key appearing during reconcile is not silently shredded
- **WHEN** a per-tenant key first appears as a newcomer during reconciliation and `WithSharedKeyGuard()` is set without `AllowSharedKeyRevocation()`
- **THEN** it is not revoked; `Erase` reports a `SharedKeyError` and `Partial=true` instead of shredding co-tenant subjects

#### Scenario: Explicit override still allows reconcile revocation
- **WHEN** both `WithSharedKeyGuard()` and `AllowSharedKeyRevocation()` are set
- **THEN** the newcomer key is revoked and reported in `ErasureResult.KeysRevoked`

### Requirement: Failed() accounts for skipped subject stores
`ErasureResult.Failed()` SHALL report failure when a registered subject store did not
erase the subject (e.g. its optional purger interface was not implemented and the
outcome was `Skipped`). Such an outcome SHALL make `Failed()` true or set `Partial` and
append an error, so a caller checking `Failed()`/`err` cannot mistake residual PII for a
completed erasure.

#### Scenario: A skipped sibling store is not reported as success
- **WHEN** a registered subject store returns a `Skipped` outcome because its purger interface is unimplemented
- **THEN** `ErasureResult.Failed()` returns true (or `Partial` is set with an error), not a clean success

### Requirement: Erasure marker is not duplicated on idempotent re-run
`appendMarker` SHALL NOT append a duplicate erasure marker when `Erase` is re-run for an
already-erased subject. A single logical erasure SHALL yield at most one marker per
subject in the marker stream.

#### Scenario: Re-running Erase does not double-append a marker
- **WHEN** `Erase` is re-invoked for a subject that already has an erasure marker
- **THEN** no second marker is appended and the accountability stream keeps exactly one marker for the erasure

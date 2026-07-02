# Design

Principles carried from the base change: **optional, type-asserted interfaces**
(never widen a required interface — the injected adapters/clients must keep
compiling), **zero overhead when unconfigured**, **append-only** (event rows are
never mutated), and **signatures match existing code** (no ctx added where the
sibling method has none, etc.).

## 1. Revocation state (soft vs permanent) — `key-revocation`, `erasure-verification`

Problem: `IsRevoked` returns `true` for both soft- and hard-revoked keys, so
`Verify` certifies a still-recoverable (soft-revoked, in-window) key as erased; and
soft-revoke never clears key material even after the window.

Decision:
- Add `encryption.RevocationState` (`NotRevoked`/`SoftRevoked`/`Revoked`) and an
  **optional** `StatefulRevocable interface { RevocationState(keyID string) (RevocationState, error) }`.
  Package helper `encryption.GetRevocationState(p, keyID)` type-asserts it and
  falls back to `IsRevoked` → `{Revoked, NotRevoked}` for providers that don't
  implement it (KMS/Vault). Keeps `Revocable`/`IsRevoked` unchanged.
- Local provider: a single internal `stateLocked(keyID)` computes state and
  **promotes** an expired soft-revocation to hard on access — clears + deletes the
  key bytes, moves it to `revoked`. `getKey`, `IsRevoked`, and `RevocationState`
  all route through it, so "permanent after the window" finally shreds material
  (not just gates decryption). This is lazy promotion (no background reaper — go-mink
  adds no goroutines; consistent with the rest of the provider).
- `Verify` uses `GetRevocationState`: only `Revoked` counts as erased;
  `SoftRevoked` → `ResidualRecoverable` (new field) and `Verified=false`.
- KMS/Vault gain no soft-revoke; `encryption.SoftRevoke`/`Unrevoke` helpers return
  `ErrRevocationUnsupported` for them (vs today's silent `ok=false`).

## 2. Sibling-store erasure — `subject-erasure` (NEW)

Problem: audit trail, saga state, snapshots, and decrypting outbox transforms hold
plaintext PII the eraser never reaches.

Decision — one unifying seam:
```go
type SubjectErasable interface {
    EraseSubject(ctx context.Context, subjectID string, footprint *SubjectFootprint) (SubjectErasureOutcome, error)
    ErasableName() string
}
```
`DataEraser.WithSubjectStore(...SubjectErasable)` registers them; `Erase` runs each
after key-revocation + read-model redaction (so a partial failure is non-fatal and
reported, symmetric with hooks), recording `ErasureResult.SubjectStores []SubjectErasureOutcome`.
The `footprint` is passed so a store can key off the subject's stream ids (snapshots)
without re-resolving.

Built-in erasers (constructors returning `SubjectErasable`):
- **Audit** — new optional adapter sub-interface `adapters.SubjectAuditPurger { DeleteAuditBySubject(ctx, subjectID) (int64, error) }` on memory + postgres audit stores; deletes rows where `actor == subjectID OR aggregate_id == subjectID` (documented matching; the `mink_audit` "immutable" framing is relaxed for the narrow GDPR-erasure case). Wrapper `mink.NewAuditSubjectEraser(store)`.
- **Saga** — new optional `adapters.SubjectSagaPurger { DeleteSagasBySubject(ctx, subjectID) (int64, error) }`; deletes saga rows where `correlation_id == subjectID`. Wrapper `mink.NewSagaSubjectEraser(store)`.
- **Snapshot** — `mink.NewSnapshotSubjectEraser(snapshotAdapter)` deletes snapshots for each `footprint.Streams` via the existing `DeleteSnapshot(ctx, streamID)`.

The outbox `Transform`-decrypts case is documented (not code-fixed): a transform that
emits plaintext is the caller's construction; guidance says shred-friendly transforms
should emit ciphertext or the caller should register a `SubjectErasable` for its sink.

Append-only note: these are all **read-side** stores, so subject-scoped deletion does
not violate the event-log invariant.

## 3. Blast-radius guard — `data-erasure`

Problem: with `WithTenantKeyResolver`, one key protects a whole tenant; erasing one
subject revokes it and destroys every subject under it, silently.

Decision: opt-in `WithSharedKeyGuard()`. Before revoking, run one exclusivity scan:
a key is **shared** if any event under it is tagged for a subject `!= target` (or is
untagged). If any key-to-revoke is shared and `AllowSharedKeyRevocation()` is not
set, return `*SharedKeyError` (`ErrSharedKeyRevocation`) listing the shared keys and
a sample of co-tenant subjects — **before** the irreversible step. Zero overhead when
the guard is off. O(all events) when on, acceptable (erasure is rare, already scans).

## 4. Accountability durability & TOCTOU — `data-erasure`

- `WithStrictAccountability()`: after the (idempotent) revoke, a marker-append or
  certificate-emit failure returns a non-nil error (the caller re-runs Erase — revoke
  is idempotent — to finish the receipt). Default stays best-effort/non-fatal.
- Gate `cert.Verified` on `MarkerWritten` when a marker stream is configured.
- TOCTOU: after revoke, re-resolve once; revoke any newly-appeared keys; if the key
  set still grew on the second look, set `result.Partial=true` + a warning error.
  Document that race-free erasure requires quiescing the subject's writes.

## 5. Subject index + backfill — `subject-discovery`

Problem: no adapter implements `SubjectIndexAdapter` → every op is O(all events); and
events written before tagging are permanently `Partial` with no backfill.

Decision (optional companion table, no forced schema change):
- `SubjectIndexWriter interface { IndexSubjects(ctx, streamID string, subjectIDs []string) error }`
  (write) alongside the existing `SubjectIndexAdapter` (read). Postgres impl backs a
  `mink_subject_index(subject_id, stream_id)` table (migration shipped, created only
  when the feature is used); memory impl backs a map.
- When a `SubjectIndexWriter` is wired, the append path writes the derived subjects
  to the index in the same unit as the event (best-effort, logged) so new events are
  indexed; `mink.BackfillSubjectIndex(ctx, store, tagger, writer)` scans history and
  populates it for pre-adoption events — the real "migration step" the design doc
  promised. Resolver prefers the index (O(subject)) and falls back to scan.
- Because the index is authoritative for "which streams touch a subject," a fully
  back-filled index lets `Resolve` return `Partial=false` for historical subjects.

## 6. `ReEncryptStream` — `key-lifecycle`

- Append to the destination with `expectedVersion = NoStream` so a re-run doesn't
  duplicate (idempotent-guard; a second run errors instead of double-copying).
- Strip `$encryption_*` markers from carried metadata before re-append so a changed
  field-config can't leave stale markers.
- Return `(copied int, oldKeyIDs []string, err error)` and document loudly: the source
  stream and its old-key-recoverable PII survive until the caller retires the source
  and revokes the old key(s) — this function alone does not erase anything.

## 7. Signature-compatibility summary

All new interfaces (`StatefulRevocable`, `SubjectErasable`, `SubjectAuditPurger`,
`SubjectSagaPurger`, `SubjectIndexWriter`) are optional and type-asserted; no existing
required interface changes. New `DataEraser`/provider behavior is gated behind new
`WithX()` options or new package helpers, so existing callers are unaffected and the
zero-overhead-when-unused invariant holds.

# Fix correctness bugs from the 2026-07-03 review

## Why

A max-effort review (10 finder + 10 independent verifier agents over ~30k LOC of
production Go + CLI) surfaced **18 independently-confirmed correctness bugs**. Each
looks complete but fails on a specific input, timing, or configuration, and several
silently lose data or attest to work that did not fully happen. They cluster into a
few throughlines:

- **A mistyped filter becomes a whole-table `DELETE`.** The read-model query builder
  silently drops any filter whose field does not resolve to a column, so a query
  built from an unknown/misspelled field yields an empty `WHERE` — turning
  `DeleteMany` into an unqualified `DELETE FROM table` and `Find`/`Count` into
  full scans. This directly contradicts the builder's own invariant comment.
- **Catch-up → live handoff gaps drop committed events.** The PostgreSQL
  subscriptions advance their cursor to the last-seen `global_position`, so a
  transaction that commits out of order (lower `BIGSERIAL`, later commit) is skipped
  forever. The in-memory adapter releases its snapshot lock before registering the
  subscriber, losing any event appended in that window. Live projections have no
  catch-up and silently drop events delivered while a worker is not `Running`.
- **GDPR crypto-shredding attests to erasures that did not fully happen.** Export
  emits a still-encrypted event as a non-redacted record (ciphertext in the payload)
  when a decryption-error handler is configured; a merely *disabled* (reversible) KMS
  key is reported as permanently revoked; the shared-key blast-radius guard is
  bypassed during reconciliation; and `ErasureResult.Failed()` reports success while a
  registered sibling store still holds the subject's PII.
- **Saga & outbox reliability holes.** `WithSagaRetryAttempts(0)` is unguarded and
  silently drops events while advancing the position; a context cancellation during
  graceful shutdown is misread as a business failure and drives false compensation;
  and the webhook publisher marks a message delivered on any `< 400` status, including
  unfollowed `3xx`.

This change fixes them. It adds **no features**, makes **no breaking public API
changes** (additive only — new typed errors, an option clamp, a `--force` flag, new
optional behavior behind existing config), and requires **no DB-schema changes**. The
append-only event-log and zero-overhead-when-unconfigured invariants are preserved
throughout — every fix targets read-side behavior, validation, or copy/ordering
semantics, never event mutation.

## What Changes

### Required

- **Read-model DELETE/scan guard** (`read-model-store`, NEW). `buildWhereClause`
  SHALL return a typed `ErrUnknownFilterField` when a caller-supplied, non-empty
  filter set resolves to zero column conditions, so `DeleteMany` can never issue an
  unqualified `DELETE` and `Find`/`Count` never silently scan all rows. (Finding 1 —
  Critical)
- **Gapless subscriptions** (`event-subscriptions`, NEW). PostgreSQL `SubscribeAll`
  and `SubscribeCategory` SHALL never skip a committed event by advancing past an
  out-of-order-committed `global_position`; the in-memory adapter SHALL snapshot
  history and register the subscriber atomically. (Findings 2, 5)
- **Saga reliability** (`saga-orchestration`, NEW). `WithSagaRetryAttempts` SHALL
  clamp to `>= 1`; a `context.Canceled`/`DeadlineExceeded` from dispatch during
  `Stop()` SHALL leave the saga `Running` rather than driving compensation on a dead
  context. (Findings 3, 6)
- **Projection recovery** (`projection-engine`, NEW). A checkpoint-load error at
  worker startup SHALL fail the worker rather than silently resuming from position 0.
  (Finding 4)
- **Outbox delivery correctness** (`outbox-publishing`, NEW). The webhook publisher
  SHALL treat only `2xx` responses as delivered. (Finding 7)
- **Erasure completeness** (`data-erasure`, MODIFIED). The shared-key guard SHALL also
  apply to newly-appeared keys during reconciliation; `ErasureResult.Failed()` SHALL
  account for registered subject stores that were skipped. (Findings 8, 11)
- **Export never leaks ciphertext** (`data-export`, NEW). Export SHALL independently
  redact any event whose fields remain encrypted, regardless of the configured
  decryption-error handler. (Finding 9)
- **Revocation permanence** (`key-revocation`, MODIFIED). A merely-disabled (reversible)
  KMS key SHALL NOT be reported as revoked/erased; `RevokeKey` SHALL schedule deletion
  or return an error. (Finding 10)

### Good-to-have

- **CLI safety** (`cli-tooling`, NEW). `mink generate` SHALL refuse to overwrite an
  existing file unless `--force` is set; `mink migrate down` SHALL treat a
  migration-record removal failure as fatal, symmetric with `up`. (Findings 12, 18)
- **Projection edge cases** (`projection-engine`, MODIFIED). Live projections SHALL NOT
  silently drop events delivered while a worker is not `Running`; the poison event
  reported to `OnPoisonEvent` SHALL come from the filtered slice actually applied.
  (Findings 13, 15)
- **Adapter data integrity** (`event-store`, NEW). The in-memory adapter SHALL
  deep-copy event `Data` and `Metadata.Custom` on append so a caller's later mutation
  cannot corrupt stored/loaded events. (Finding 14)
- **Category LIKE escaping** (`event-subscriptions`, MODIFIED). `SubscribeCategory`
  SHALL escape `%`/`_` in the category prefix. (Finding 16)
- **Erasure marker idempotency** (`data-erasure`, MODIFIED). Re-running `Erase` for an
  already-erased subject SHALL NOT append a duplicate marker. (Finding 17)

## Non-Goals

- **Re-architecting subscriptions or projections.** This change closes the identified
  gaps (a safe watermark, atomic snapshot+register, checkpoint-load failure handling);
  it does not replace polling with logical replication or add a general live-projection
  catch-up engine beyond guarding/documenting the contract.
- **Mutating or deleting event rows.** The append-only invariant holds everywhere. The
  read-model DELETE guard is about *refusing* a dangerous query, not new deletion
  semantics; sibling-store and read-model changes stay read-side only.
- **Recoverable (soft) revoke on KMS/Vault.** Finding 10 only makes a *disabled* key
  stop counting as permanently erased; it does not add cloud-provider soft-revoke
  (tracked separately).
- **New encryption of the audit trail / saga state / snapshots.** Out of scope; this
  change fixes erasure/accounting correctness, not at-rest encryption of derived stores.
- **CLI UX beyond safety.** The `--force` flag and fatal `migrate down` record failure
  are safety fixes, not a broader CLI redesign.

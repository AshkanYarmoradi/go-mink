# erasure-side-effects Specification

## Purpose
TBD - created by archiving change gdpr-erasure-and-retention. Update Purpose after archive.
## Requirements
### Requirement: Erasure side-effect hooks for app-owned data
`DataEraser` SHALL support registering optional erasure hooks via `WithErasureHook(ErasureHook)`, where `ErasureHook` is `func(ctx, ErasureContext) error`, invoked during `Erase` so the application can erase PII it owns *outside* the event store — blob/object storage, generated exports/PDFs, caches, search indexes, short-URLs. Hooks are opt-in with zero overhead when none are registered.

#### Scenario: App erases external artifacts during erasure
- **WHEN** an erasure hook is registered and a subject is erased
- **THEN** the hook runs with the subject's `ErasureContext` (subject id, affected streams, key ids) so the app can delete its own external artifacts

#### Scenario: No hooks means no overhead
- **WHEN** no erasure hooks are registered
- **THEN** `Erase` behaves exactly as before with no extra calls, preserving the zero-overhead-when-unused invariant

### Requirement: Side-effect outcomes are captured, not fatal
Hook outcomes (success/failure per hook) SHALL be recorded on `ErasureResult`, and a hook failure SHALL be reported rather than aborting the erasure, consistent with the eraser's partial-failure semantics. The erasure certificate, when enabled, SHALL include side-effect outcomes without embedding any erased PII.

#### Scenario: One failing hook does not block erasure
- **WHEN** one of several erasure hooks returns an error
- **THEN** the crypto-shred and the other hooks still complete, and the failure is captured in `ErasureResult.Errors`

#### Scenario: Certificate records side-effects without PII
- **WHEN** a certificate is emitted after an erasure that ran side-effect hooks
- **THEN** it lists which side-effect domains were cleaned, containing no erased PII


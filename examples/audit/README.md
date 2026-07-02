# Command Audit Logging Example

> Record who did what, when, and with what outcome — an immutable audit trail for every command.

Regulated and security-sensitive systems need a tamper-evident record of every action taken and by whom, including the ones that failed. go-mink's `AuditMiddleware` writes one immutable entry per command dispatched through the bus, capturing the actor, command type, success/failure, error, and duration. Because it lives in the middleware pipeline, auditing is transparent to your handlers and applies uniformly to every command.

## What this demonstrates
- **Audit middleware** — `AuditMiddleware(DefaultAuditConfig(store))` records one entry per command with no changes to handler code.
- **Success and failure captured** — both a passing `CreateAccount` and a failing `Withdraw` are recorded, the latter with its error message.
- **Nested recovery** — `RecoveryMiddleware` sits inside the audit middleware, so even a panicking handler is still recorded as a failed entry.
- **Actor attribution** — `WithActor(ctx, "user-42")` stamps every audit entry with who initiated the command.
- **Selective skipping** — `cfg.SkipCommands = []string{"HealthCheck"}` keeps noisy commands out of the trail.
- **Querying the trail** — `AuditQuery{}` (zero value) returns all entries, most recent first.

## Running
```bash
go run ./examples/audit
```
No infrastructure required — the audit store is in-memory (`memory.NewAuditStore`). For production, swap in `postgres.NewAuditStore(db, ...)` and call its `Initialize` (the audit table is created by the store, not by the adapter's migrations).

## What happens
1. An in-memory `AuditStore` and a `HandlerRegistry` with three handlers are created: `CreateAccount` (returns success), `Withdraw` (always fails with "insufficient funds"), and `HealthCheck` (success).
2. A `CommandBus` is built with `AuditMiddleware` wrapping `RecoveryMiddleware`; the audit config skips `HealthCheck` and enables `IncludeMetadata`.
3. `WithActor(ctx, "user-42")` identifies the caller, then three commands are dispatched: `CreateAccount` (succeeds), `Withdraw` (fails — the expected error is printed), and `HealthCheck` (skipped from the audit trail).
4. `auditStore.Find(ctx, AuditQuery{})` returns the trail. Only **two** entries appear — `HealthCheck` was skipped — printed with command type, actor, success flag, `OK`/`FAILED` status, error message, and duration in milliseconds.

## Key APIs
- `mink.AuditMiddleware(config)` — middleware that writes an audit entry per command.
- `mink.DefaultAuditConfig(store)` — sensible defaults; here extended with `SkipCommands` and `IncludeMetadata`.
- `memory.NewAuditStore()` — in-memory audit store for tests/examples (production: `postgres.NewAuditStore`).
- `mink.WithActor(ctx, actor)` — attach the acting principal to the context so it lands on each entry.
- `mink.RecoveryMiddleware()` — converts handler panics into errors; nested inside audit so panics are still recorded.
- `mink.AuditQuery{}` — filter/paginate the audit trail; the zero value returns everything.
- `mink.NewHandlerRegistry()` / `mink.RegisterGenericHandler(...)` — register the command handlers dispatched through the bus.

## Related
- **Examples:** [full-ecommerce](../full-ecommerce) · [cqrs-postgres](../cqrs-postgres)
- **Docs:** [Audit Logging](https://go-mink.dev/docs/advanced/audit-logging) · [API reference](https://pkg.go.dev/go-mink.dev)

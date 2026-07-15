## ADDED Requirements

### Requirement: Transient errors retry independently of the poison budget

The async projection worker SHALL support an optional classifier
`AsyncOptions.ErrorClassifier func(error) ErrorClass`, where `ErrorClass` is
`ErrorClassPoison` (the zero value) or `ErrorClassTransient`. When a processing error
classifies as `ErrorClassTransient`, the worker SHALL retry it with backoff but MUST NOT
count it against the poison retry budget (`MaxRetries` / `RetryPolicy`), so a transient
error never reaches `OnPoisonEvent` and never faults the worker. When it classifies as
`ErrorClassPoison`, the worker SHALL account for it exactly as today (consume the budget;
on exhaustion invoke `OnPoisonEvent` or transition to `Faulted`). Backoff delay SHALL be
driven by consecutive errors of any class, and both counters SHALL reset when a batch
succeeds. When `ErrorClassifier` is nil, every error SHALL be treated as `ErrorClassPoison`,
making behavior and overhead identical to the prior release.

#### Scenario: A transient error retries past MaxRetries without faulting

- **WHEN** `ErrorClassifier` classifies the error as `ErrorClassTransient` and the error persists for more consecutive cycles than `MaxRetries`
- **THEN** the worker keeps retrying with backoff, stays out of `Faulted`, and never calls `OnPoisonEvent` — the poison budget is untouched

#### Scenario: A poison error still exhausts the budget

- **WHEN** `ErrorClassifier` classifies the error as `ErrorClassPoison` (or is nil) and the error persists past `MaxRetries`
- **THEN** the worker exhausts the budget and either skips via `OnPoisonEvent` (when set and it returns nil) or transitions to `Faulted` — unchanged from today

#### Scenario: Interleaved transient and poison errors only count poison

- **WHEN** a projection alternately returns transient and poison errors before finally succeeding
- **THEN** only the poison-classified errors advance the poison budget; the transient ones drive backoff but not the budget, and a successful batch resets both counters

#### Scenario: Nil classifier reproduces current behavior

- **WHEN** `AsyncOptions.ErrorClassifier` is nil (the default)
- **THEN** every error counts against the poison budget exactly as before, with no added allocation or classification work

#### Scenario: Backoff and observability still fire for transient errors

- **WHEN** a transient error is retried
- **THEN** the exponential backoff still applies and caps at `maxDelay`, and the existing power-of-2 error log and `ProjectionMetrics.RecordError` still fire — a transient outage is visible, not silent

### Requirement: A default classifier honors standard transient signals

The package SHALL provide `func DefaultErrorClassifier(err error) ErrorClass` returning
`ErrorClassTransient` when `err`, or any error in its `Unwrap` chain, satisfies
`errors.Is(err, ErrTransient)`, or implements `interface{ Retryable() bool }` returning true,
or implements `interface{ Temporary() bool }` returning true; otherwise `ErrorClassPoison`.
The package SHALL export the `ErrTransient` sentinel and the `Retryable` interface so callers
can mark their own errors, and `DefaultErrorClassifier` MUST be usable as, or composable into,
a custom classifier. `DefaultErrorClassifier` SHALL NOT auto-classify `context.DeadlineExceeded`
as transient.

#### Scenario: An ErrTransient-wrapped error is transient

- **WHEN** a projection's `Apply` returns `fmt.Errorf("db down: %w", mink.ErrTransient)` and `DefaultErrorClassifier` is configured
- **THEN** the error classifies as `ErrorClassTransient` and is retried independently of the poison budget

#### Scenario: A Temporary()/Retryable() error is transient

- **WHEN** the error implements `Temporary() bool` (the net.Error idiom) or the exported `Retryable() bool` and returns true
- **THEN** `DefaultErrorClassifier` returns `ErrorClassTransient`

#### Scenario: An ordinary deterministic error is poison

- **WHEN** a projection returns a plain error (e.g. a JSON unmarshal failure on a genuinely malformed event) that matches none of the transient signals
- **THEN** `DefaultErrorClassifier` returns `ErrorClassPoison` and the event flows through the normal poison budget, preserving poison-event handling for real poison events

#### Scenario: Context deadline is not silently transient

- **WHEN** a batch fails with `context.DeadlineExceeded` (e.g. from `BatchTimeout`)
- **THEN** `DefaultErrorClassifier` returns `ErrorClassPoison` (it does not mask a possibly-hung poison event); a caller who wants deadlines treated as transient must classify explicitly

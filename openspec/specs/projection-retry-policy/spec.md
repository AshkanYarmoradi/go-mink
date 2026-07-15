# projection-retry-policy Specification

## Purpose
Defines a single, consistent retry-budget convention for the async projection worker — a positive budget means a bounded number of attempts, while a non-positive budget means retry indefinitely — and provides explicit `RetryForever` / `NoRetry` policies so callers never rely on the `0`-count sentinel to express "retry forever."

## Requirements
### Requirement: A non-positive retry budget means retry indefinitely, consistently

Every retry-budget input to the async projection worker SHALL follow one convention: a
**positive** budget means "that many attempts, then stop"; a **non-positive** budget
(`0` or negative) means "retry indefinitely." This SHALL hold for the built-in
`ExponentialBackoffRetry(maxRetries, …)` policy, for the nil-policy `AsyncOptions.MaxRetries`
path, and for `RetryForever`, so the three cannot disagree. Specifically,
`exponentialBackoffRetry.ShouldRetry(attempt, err)` SHALL return `false` when `err == nil`,
`true` when `err != nil` and `maxRetries <= 0`, and `attempt < maxRetries` otherwise. The
`RetryPolicy` interface (`ShouldRetry`/`Delay`) SHALL be unchanged, and `RetryPolicy` SHALL
continue to take precedence over `MaxRetries` when both are set.

#### Scenario: ExponentialBackoffRetry(0, …) retries forever instead of never

- **WHEN** an async projection is registered with `RetryPolicy: ExponentialBackoffRetry(0, base, max)` and its `Apply` keeps returning a non-nil error
- **THEN** the worker keeps retrying with backoff indefinitely (it does NOT give up on the first error), reversing the prior footgun where a non-positive count meant "never retry"

#### Scenario: A negative count is also treated as unlimited

- **WHEN** `ExponentialBackoffRetry(-1, base, max)` is configured and errors persist
- **THEN** the worker retries indefinitely, identically to a `0` count

#### Scenario: A positive count is unchanged

- **WHEN** `ExponentialBackoffRetry(3, base, max)` is configured and every attempt errors
- **THEN** the worker retries after the 1st and 2nd consecutive errors and gives up at the 3rd (three attempts), exactly as before this change

#### Scenario: Default options are unaffected

- **WHEN** an async projection is registered with `DefaultAsyncOptions()` (`ExponentialBackoffRetry(3, …)`, `MaxRetries: 3`)
- **THEN** its retry behavior is byte-for-byte identical to the prior release — the convention change only affects callers who passed a non-positive count

#### Scenario: MaxRetries path already meant forever and stays that way

- **WHEN** `RetryPolicy` is nil and `MaxRetries <= 0`
- **THEN** the worker retries indefinitely, as it already did — the change makes the policy path match this pre-existing `MaxRetries` convention rather than changing it

### Requirement: An explicit RetryForever policy expresses unlimited retry

The package SHALL provide `func RetryForever(baseDelay, maxDelay time.Duration) RetryPolicy`
that retries on every non-nil error without limit, with exponential backoff between attempts
capped at `maxDelay`, so callers can express "retry forever with backoff" without relying on
the `0`-count sentinel. `NoRetry()` SHALL remain the single canonical "never retry" policy.

#### Scenario: RetryForever never gives up

- **WHEN** an async projection uses `RetryForever(base, max)` and its `Apply` errors on every attempt
- **THEN** the worker never transitions to `Faulted` from budget exhaustion; it keeps retrying with capped backoff

#### Scenario: NoRetry is the way to never retry

- **WHEN** a caller wants a projection to stop on the first error
- **THEN** `NoRetry()` provides it (`ShouldRetry` always false), and the documentation directs callers there rather than to `ExponentialBackoffRetry(0, …)`

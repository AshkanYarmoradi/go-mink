## ADDED Requirements

### Requirement: Saga retry attempts are at least one
`WithSagaRetryAttempts(n)` SHALL clamp the configured attempts to a minimum of 1, so a
saga event is always attempted at least once. A configured value of 0 SHALL NOT cause
the processing loop to skip the event, drop it silently, or advance the position past an
unprocessed event.

#### Scenario: Zero retry attempts still processes the event
- **WHEN** the saga manager is configured with `WithSagaRetryAttempts(0)` and an event arrives
- **THEN** the event is dispatched at least once and the position advances only after a real attempt, not over a silently-dropped event

### Requirement: Context cancellation during dispatch does not trigger compensation
The saga manager SHALL NOT treat a command-dispatch failure of `context.Canceled` or
`context.DeadlineExceeded` (e.g. during a graceful `Stop()`) as a business failure. It
SHALL leave the saga `Running` and SHALL NOT run compensation, so the saga is not
persisted as `CompensationFailed` and a restart can resume it.

#### Scenario: Graceful shutdown mid-dispatch leaves the saga runnable
- **WHEN** the context is cancelled while dispatching a saga command
- **THEN** the saga stays `Running`, no compensation is dispatched, and it is not persisted as `CompensationFailed`

#### Scenario: A real step failure still compensates
- **WHEN** a command dispatch fails with a non-context business error
- **THEN** the saga runs its compensation as before

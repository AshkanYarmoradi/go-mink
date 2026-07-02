# BDD Testing Example

> Test event-sourced aggregates with Given/When/Then using the go-mink `bdd` package.

Behavior-Driven Development expresses aggregate behavior as readable specifications: given a history of past events, when a command runs, then specific new events are emitted — or a specific error is returned. This example specs out a `BankAccount` aggregate (deposits, withdrawals, daily limits, closing) so its rules are verified without any database or serialization.

## What this demonstrates
- **Given / When / Then** — `bdd.Given(t, account, pastEvents...)` seeds state, `.When(func() error {...})` runs a command, and `.Then(expectedEvent)` asserts the uncommitted events produced.
- **Error assertions** — `.ThenError(err)` verifies a command rejects invalid operations (insufficient funds, exceeded daily limit, closed account).
- **State reconstruction from events** — the fixture replays the `Given` events through `ApplyEvent` to rebuild the aggregate before the command runs.
- **Rich domain rules** — `BankAccount` enforces positive amounts, balance sufficiency, per-day withdrawal limits, and closed-account guards.
- **Table-driven subtests** — ~12 `t.Run` cases across opening, deposits, withdrawals, daily limits, and closing, plus a full-lifecycle scenario.

## Running
```bash
go test -v ./examples/bdd-testing
```
No infrastructure required — uses the in-memory adapter.

This is a test file, not a program — run it with `go test`, not `go run`.

## What happens
Each subtest builds a `BankAccount`, seeds it with prior events, executes one command, and asserts the outcome:
1. **Opening** — an empty account opens and emits `AccountOpened`.
2. **Deposits** — a deposit emits `MoneyDeposited`; negative amounts fail with `ErrNegativeAmount`; depositing to a closed account fails with `ErrAccountClosed`.
3. **Withdrawals** — a funded account withdraws and emits `MoneyWithdrawn`; over-balance fails with `ErrInsufficientFunds`; exceeding the daily limit fails with `ErrDailyLimitExceeded`; a closed account fails with `ErrAccountClosed`.
4. **Daily limit** — `SetDailyLimit` emits `DailyLimitSet`; multiple withdrawals succeed while their running total stays under the limit.
5. **Closing** — an open account closes and emits `AccountClosed`; closing an already-closed account fails with `ErrAccountClosed`.
6. **Complete lifecycle** — open → deposit → withdraw → close, asserting balances (1000 → 750) and the final closed state.

Because timestamps are set at command time, event assertions copy the generated time from `account.UncommittedEvents()[0]` so comparisons stay deterministic.

## Key APIs
- `bdd.Given(t, aggregate, events ...interface{}) *TestFixture` — seed the aggregate with prior events.
- `(*TestFixture).When(func() error) *TestFixture` — execute the command under test.
- `(*TestFixture).Then(expectedEvents ...interface{})` — assert the emitted uncommitted events.
- `(*TestFixture).ThenError(err error)` — assert the command returned the expected error.
- `mink.AggregateBase` — embedded base providing `SetID`, `SetType`, `Apply`, `IncrementVersion`, `UncommittedEvents`, `ClearUncommittedEvents`.
- `ApplyEvent(event interface{}) error` — the aggregate method that folds events into state.

## Related
- **Examples:** [full-ecommerce](../full-ecommerce) · [basic](../basic) · [cqrs-postgres](../cqrs-postgres)
- **Docs:** [Testing](https://go-mink.dev/docs/guide/testing) · [API reference](https://pkg.go.dev/go-mink.dev/testing/bdd)

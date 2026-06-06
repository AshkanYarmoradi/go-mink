// Package main demonstrates the command Audit Logging middleware.
//
// This example shows:
//   - Wiring AuditMiddleware into a CommandBus (with RecoveryMiddleware nested
//     inside, so even a panicking handler is recorded as a failed audit entry)
//   - Auditing successful AND failing commands
//   - Skipping selected command types from the audit trail
//   - Identifying the actor via WithActor on the context
//   - Querying the audit trail with AuditQuery
//
// It uses the in-memory audit store, so it runs with NO database:
//
//	go run ./examples/audit
//
// For production, swap the in-memory store for the PostgreSQL store. The audit
// table is created by store.Initialize (it is NOT part of the adapter's
// migrations):
//
//	db, _ := sql.Open("postgres", connStr)            // import _ "github.com/lib/pq"
//	auditStore := postgres.NewAuditStore(db,
//		postgres.WithAuditSchema("public"),
//		postgres.WithAuditTable("mink_audit"),
//	)
//	_ = auditStore.Initialize(ctx)
//	// then: mink.AuditMiddleware(mink.DefaultAuditConfig(auditStore))
package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"go-mink.dev"
	"go-mink.dev/adapters/memory"
)

// =============================================================================
// Commands
// =============================================================================

// CreateAccountCommand succeeds and is audited.
type CreateAccountCommand struct {
	mink.CommandBase
	Owner string `json:"owner"`
}

func (c CreateAccountCommand) CommandType() string { return "CreateAccount" }
func (c CreateAccountCommand) AggregateID() string { return c.Owner }
func (c CreateAccountCommand) Validate() error {
	if c.Owner == "" {
		return mink.NewValidationError("CreateAccount", "Owner", "owner is required")
	}
	return nil
}

// WithdrawCommand fails in the handler (insufficient funds) and is audited as a failure.
type WithdrawCommand struct {
	mink.CommandBase
	AccountID string `json:"accountId"`
	Amount    int    `json:"amount"`
}

func (c WithdrawCommand) CommandType() string { return "Withdraw" }
func (c WithdrawCommand) AggregateID() string { return c.AccountID }
func (c WithdrawCommand) Validate() error     { return nil }

// HealthCheckCommand is excluded from the audit trail via SkipCommands.
type HealthCheckCommand struct {
	mink.CommandBase
}

func (c HealthCheckCommand) CommandType() string { return "HealthCheck" }
func (c HealthCheckCommand) Validate() error     { return nil }

// =============================================================================
// Main
// =============================================================================

func main() {
	fmt.Println("=== go-mink: Audit Logging Middleware Example ===")
	fmt.Println()

	ctx := context.Background()

	// In-memory audit store (no database needed).
	auditStore := memory.NewAuditStore()

	// Register command handlers.
	registry := mink.NewHandlerRegistry()

	mink.RegisterGenericHandler(registry, func(ctx context.Context, cmd CreateAccountCommand) (mink.CommandResult, error) {
		// Pretend we persisted a new account aggregate at version 1.
		return mink.NewSuccessResult(cmd.Owner, 1), nil
	})

	mink.RegisterGenericHandler(registry, func(ctx context.Context, cmd WithdrawCommand) (mink.CommandResult, error) {
		// Always fail to demonstrate auditing of failures.
		err := errors.New("insufficient funds")
		return mink.NewErrorResult(err), err
	})

	mink.RegisterGenericHandler(registry, func(ctx context.Context, cmd HealthCheckCommand) (mink.CommandResult, error) {
		return mink.NewSuccessResult("", 0), nil
	})

	// Build the bus. Recovery sits inside Audit so that even a panicking handler
	// is recorded as a failed audit entry.
	bus := mink.NewCommandBus(
		mink.WithHandlerRegistry(registry),
		mink.WithMiddleware(
			mink.AuditMiddleware(func() mink.AuditConfig {
				cfg := mink.DefaultAuditConfig(auditStore)
				cfg.SkipCommands = []string{"HealthCheck"} // not audited
				cfg.IncludeMetadata = true
				return cfg
			}()),
			mink.RecoveryMiddleware(),
		),
	)

	// Identify who is performing the commands.
	ctx = mink.WithActor(ctx, "user-42")

	// 1. A successful command (audited).
	fmt.Println("Dispatching CreateAccount (success)...")
	if _, err := bus.Dispatch(ctx, CreateAccountCommand{
		CommandBase: mink.CommandBase{CommandID: "cmd-create-1"},
		Owner:       "acct-1001",
	}); err != nil {
		log.Printf("   create failed: %v", err)
	}

	// 2. A failing command (audited as a failure).
	fmt.Println("Dispatching Withdraw (failure)...")
	if _, err := bus.Dispatch(ctx, WithdrawCommand{
		CommandBase: mink.CommandBase{CommandID: "cmd-withdraw-1"},
		AccountID:   "acct-1001",
		Amount:      999,
	}); err != nil {
		fmt.Printf("   (expected) withdraw failed: %v\n", err)
	}

	// 3. A skipped command type (NOT audited).
	fmt.Println("Dispatching HealthCheck (skipped from audit)...")
	if _, err := bus.Dispatch(ctx, HealthCheckCommand{}); err != nil {
		log.Printf("   health check failed: %v", err)
	}

	// Query and print the audit trail (most recent first by default).
	fmt.Println()
	fmt.Println("--- Audit trail ---")
	entries, err := auditStore.Find(ctx, mink.AuditQuery{})
	if err != nil {
		log.Fatalf("failed to query audit trail: %v", err)
	}

	fmt.Printf("%d entr%s recorded (HealthCheck was skipped):\n", len(entries), plural(len(entries)))
	for _, e := range entries {
		status := "OK"
		if !e.Success {
			status = "FAILED"
		}
		fmt.Printf("  - %-14s actor=%-8s success=%-5v status=%-6s error=%q duration=%dms\n",
			e.CommandType, e.Actor, e.Success, status, e.Error, e.DurationMs)
	}
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

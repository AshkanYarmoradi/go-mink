// Example: BDD Testing for Aggregates
//
// This example demonstrates how to use the BDD testing utilities
// for test-driven development of event-sourced aggregates.
//
// Run with: go test -v
package bdd_example

import (
	"errors"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/testing/bdd"
)

// =============================================================================
// Domain Errors
// =============================================================================

var (
	ErrInsufficientFunds  = errors.New("insufficient funds")
	ErrAccountClosed      = errors.New("account is closed")
	ErrNegativeAmount     = errors.New("amount must be positive")
	ErrDailyLimitExceeded = errors.New("daily withdrawal limit exceeded")
)

// =============================================================================
// Events
// =============================================================================

type AccountOpened struct {
	AccountID   string    `json:"accountId"`
	CustomerID  string    `json:"customerId"`
	AccountType string    `json:"accountType"`
	OpenedAt    time.Time `json:"openedAt"`
}

type MoneyDeposited struct {
	AccountID     string    `json:"accountId"`
	Amount        float64   `json:"amount"`
	TransactionID string    `json:"transactionId"`
	DepositedAt   time.Time `json:"depositedAt"`
}

type MoneyWithdrawn struct {
	AccountID     string    `json:"accountId"`
	Amount        float64   `json:"amount"`
	TransactionID string    `json:"transactionId"`
	WithdrawnAt   time.Time `json:"withdrawnAt"`
}

type AccountClosed struct {
	AccountID string    `json:"accountId"`
	Reason    string    `json:"reason"`
	ClosedAt  time.Time `json:"closedAt"`
}

type DailyLimitSet struct {
	AccountID  string  `json:"accountId"`
	DailyLimit float64 `json:"dailyLimit"`
}

// =============================================================================
// Aggregate
// =============================================================================

type BankAccount struct {
	mink.AggregateBase
	CustomerID     string
	AccountType    string
	Balance        float64
	IsClosed       bool
	DailyLimit     float64
	WithdrawnToday float64
}

func NewBankAccount(id string) *BankAccount {
	acc := &BankAccount{
		DailyLimit: 1000, // Default daily limit
	}
	acc.SetID(id)
	acc.SetType("BankAccount")
	return acc
}

func (a *BankAccount) Open(customerID, accountType string) error {
	a.Apply(&AccountOpened{
		AccountID:   a.AggregateID(),
		CustomerID:  customerID,
		AccountType: accountType,
		OpenedAt:    time.Now(),
	})
	a.CustomerID = customerID
	a.AccountType = accountType
	return nil
}

func (a *BankAccount) Deposit(amount float64, transactionID string) error {
	if a.IsClosed {
		return ErrAccountClosed
	}
	if amount <= 0 {
		return ErrNegativeAmount
	}

	a.Apply(&MoneyDeposited{
		AccountID:     a.AggregateID(),
		Amount:        amount,
		TransactionID: transactionID,
		DepositedAt:   time.Now(),
	})
	a.Balance += amount
	return nil
}

func (a *BankAccount) Withdraw(amount float64, transactionID string) error {
	if a.IsClosed {
		return ErrAccountClosed
	}
	if amount <= 0 {
		return ErrNegativeAmount
	}
	if amount > a.Balance {
		return ErrInsufficientFunds
	}
	if a.WithdrawnToday+amount > a.DailyLimit {
		return ErrDailyLimitExceeded
	}

	a.Apply(&MoneyWithdrawn{
		AccountID:     a.AggregateID(),
		Amount:        amount,
		TransactionID: transactionID,
		WithdrawnAt:   time.Now(),
	})
	a.Balance -= amount
	a.WithdrawnToday += amount
	return nil
}

func (a *BankAccount) SetDailyLimit(limit float64) error {
	if a.IsClosed {
		return ErrAccountClosed
	}
	if limit < 0 {
		return ErrNegativeAmount
	}

	a.Apply(&DailyLimitSet{
		AccountID:  a.AggregateID(),
		DailyLimit: limit,
	})
	a.DailyLimit = limit
	return nil
}

func (a *BankAccount) Close(reason string) error {
	if a.IsClosed {
		return ErrAccountClosed
	}

	a.Apply(&AccountClosed{
		AccountID: a.AggregateID(),
		Reason:    reason,
		ClosedAt:  time.Now(),
	})
	a.IsClosed = true
	return nil
}

func (a *BankAccount) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case *AccountOpened:
		a.CustomerID = e.CustomerID
		a.AccountType = e.AccountType
	case *MoneyDeposited:
		a.Balance += e.Amount
	case *MoneyWithdrawn:
		a.Balance -= e.Amount
		a.WithdrawnToday += e.Amount
	case *AccountClosed:
		a.IsClosed = true
	case *DailyLimitSet:
		a.DailyLimit = e.DailyLimit
	}
	a.IncrementVersion()
	return nil
}

// =============================================================================
// BDD Tests
// =============================================================================

func TestBankAccount_Opening(t *testing.T) {
	t.Run("can open a new account", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account).
			When(func() error {
				return account.Open("cust-123", "savings")
			}).
			Then(&AccountOpened{
				AccountID:   "acc-001",
				CustomerID:  "cust-123",
				AccountType: "savings",
				OpenedAt:    account.UncommittedEvents()[0].(*AccountOpened).OpenedAt,
			})
	})
}

func TestBankAccount_Deposits(t *testing.T) {
	t.Run("can deposit money", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
		).
			When(func() error {
				return account.Deposit(500.00, "txn-001")
			}).
			Then(&MoneyDeposited{
				AccountID:     "acc-001",
				Amount:        500.00,
				TransactionID: "txn-001",
				DepositedAt:   account.UncommittedEvents()[0].(*MoneyDeposited).DepositedAt,
			})
	})

	t.Run("cannot deposit negative amount", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
		).
			When(func() error {
				return account.Deposit(-100.00, "txn-002")
			}).
			ThenError(ErrNegativeAmount)
	})

	t.Run("cannot deposit to closed account", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
			&AccountClosed{AccountID: "acc-001", Reason: "customer request"},
		).
			When(func() error {
				return account.Deposit(100.00, "txn-003")
			}).
			ThenError(ErrAccountClosed)
	})
}

func TestBankAccount_Withdrawals(t *testing.T) {
	t.Run("can withdraw money with sufficient funds", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
			&MoneyDeposited{AccountID: "acc-001", Amount: 1000.00, TransactionID: "txn-001"},
		).
			When(func() error {
				return account.Withdraw(200.00, "txn-002")
			}).
			Then(&MoneyWithdrawn{
				AccountID:     "acc-001",
				Amount:        200.00,
				TransactionID: "txn-002",
				WithdrawnAt:   account.UncommittedEvents()[0].(*MoneyWithdrawn).WithdrawnAt,
			})
	})

	t.Run("cannot withdraw more than balance", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
			&MoneyDeposited{AccountID: "acc-001", Amount: 100.00, TransactionID: "txn-001"},
		).
			When(func() error {
				return account.Withdraw(500.00, "txn-002")
			}).
			ThenError(ErrInsufficientFunds)
	})

	t.Run("cannot exceed daily limit", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
			&MoneyDeposited{AccountID: "acc-001", Amount: 5000.00, TransactionID: "txn-001"},
			&DailyLimitSet{AccountID: "acc-001", DailyLimit: 500.00},
		).
			When(func() error {
				return account.Withdraw(600.00, "txn-002")
			}).
			ThenError(ErrDailyLimitExceeded)
	})

	t.Run("cannot withdraw from closed account", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
			&MoneyDeposited{AccountID: "acc-001", Amount: 1000.00, TransactionID: "txn-001"},
			&AccountClosed{AccountID: "acc-001", Reason: "fraud"},
		).
			When(func() error {
				return account.Withdraw(100.00, "txn-002")
			}).
			ThenError(ErrAccountClosed)
	})
}

func TestBankAccount_DailyLimit(t *testing.T) {
	t.Run("can set daily limit", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
		).
			When(func() error {
				return account.SetDailyLimit(2000.00)
			}).
			Then(&DailyLimitSet{
				AccountID:  "acc-001",
				DailyLimit: 2000.00,
			})
	})

	t.Run("can make multiple withdrawals within limit", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
			&MoneyDeposited{AccountID: "acc-001", Amount: 5000.00, TransactionID: "dep-001"},
			&DailyLimitSet{AccountID: "acc-001", DailyLimit: 1000.00},
			&MoneyWithdrawn{AccountID: "acc-001", Amount: 400.00, TransactionID: "txn-001"},
		).
			When(func() error {
				// Should succeed: 400 + 500 = 900 < 1000 limit
				return account.Withdraw(500.00, "txn-002")
			}).
			Then(&MoneyWithdrawn{
				AccountID:     "acc-001",
				Amount:        500.00,
				TransactionID: "txn-002",
				WithdrawnAt:   account.UncommittedEvents()[0].(*MoneyWithdrawn).WithdrawnAt,
			})
	})
}

func TestBankAccount_Closing(t *testing.T) {
	t.Run("can close account", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
		).
			When(func() error {
				return account.Close("customer request")
			}).
			Then(&AccountClosed{
				AccountID: "acc-001",
				Reason:    "customer request",
				ClosedAt:  account.UncommittedEvents()[0].(*AccountClosed).ClosedAt,
			})
	})

	t.Run("cannot close already closed account", func(t *testing.T) {
		account := NewBankAccount("acc-001")

		bdd.Given(t, account,
			&AccountOpened{AccountID: "acc-001", CustomerID: "cust-123", AccountType: "checking"},
			&AccountClosed{AccountID: "acc-001", Reason: "dormant"},
		).
			When(func() error {
				return account.Close("customer request")
			}).
			ThenError(ErrAccountClosed)
	})
}

// =============================================================================
// Complex Scenario Tests
// =============================================================================

func TestBankAccount_CompleteLifecycle(t *testing.T) {
	t.Run("full account lifecycle", func(t *testing.T) {
		account := NewBankAccount("acc-lifecycle")

		// Open account
		bdd.Given(t, account).
			When(func() error {
				return account.Open("cust-lifecycle", "savings")
			}).
			Then(&AccountOpened{
				AccountID:   "acc-lifecycle",
				CustomerID:  "cust-lifecycle",
				AccountType: "savings",
				OpenedAt:    account.UncommittedEvents()[0].(*AccountOpened).OpenedAt,
			})

		// Clear uncommitted and deposit
		account.ClearUncommittedEvents()
		account.Deposit(1000.00, "initial-deposit")

		if account.Balance != 1000.00 {
			t.Errorf("Expected balance 1000.00, got %.2f", account.Balance)
		}

		// Withdraw
		account.ClearUncommittedEvents()
		account.Withdraw(250.00, "withdrawal-1")

		if account.Balance != 750.00 {
			t.Errorf("Expected balance 750.00, got %.2f", account.Balance)
		}

		// Close
		account.ClearUncommittedEvents()
		account.Close("test complete")

		if !account.IsClosed {
			t.Error("Account should be closed")
		}
	})
}

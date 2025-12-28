package mink

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test command implementations

type TestCreateOrder struct {
	CommandBase
	CustomerID string            `json:"customerId"`
	Items      []TestCommandItem `json:"items"`
}

type TestCommandItem struct {
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

func (c TestCreateOrder) CommandType() string { return "CreateOrder" }
func (c TestCreateOrder) AggregateID() string { return "" } // New aggregate

func (c TestCreateOrder) Validate() error {
	if c.CustomerID == "" {
		return NewValidationError(c.CommandType(), "CustomerID", "customer ID is required")
	}
	if len(c.Items) == 0 {
		return NewValidationError(c.CommandType(), "Items", "at least one item is required")
	}
	return nil
}

type TestAddItem struct {
	CommandBase
	OrderID  string  `json:"orderId"`
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

func (c TestAddItem) CommandType() string { return "AddItem" }
func (c TestAddItem) AggregateID() string { return c.OrderID }

func (c TestAddItem) Validate() error {
	errs := NewMultiValidationError(c.CommandType())
	if c.OrderID == "" {
		errs.AddField("OrderID", "order ID is required")
	}
	if c.SKU == "" {
		errs.AddField("SKU", "SKU is required")
	}
	if c.Quantity <= 0 {
		errs.AddField("Quantity", "quantity must be positive")
	}
	if c.Price < 0 {
		errs.AddField("Price", "price cannot be negative")
	}
	if errs.HasErrors() {
		return errs
	}
	return nil
}

type TestIdempotentCommand struct {
	CommandBase
	Key     string `json:"key"`
	Payload string `json:"payload"`
}

func (c TestIdempotentCommand) CommandType() string    { return "IdempotentCommand" }
func (c TestIdempotentCommand) Validate() error        { return nil }
func (c TestIdempotentCommand) IdempotencyKey() string { return c.Key }

func TestCommand_Interface(t *testing.T) {
	t.Run("Command interface", func(t *testing.T) {
		var cmd Command = TestCreateOrder{
			CustomerID: "cust-123",
			Items:      []TestCommandItem{{SKU: "SKU-1", Quantity: 1, Price: 10.0}},
		}
		assert.Equal(t, "CreateOrder", cmd.CommandType())
		assert.NoError(t, cmd.Validate())
	})

	t.Run("AggregateCommand interface", func(t *testing.T) {
		var cmd AggregateCommand = TestAddItem{OrderID: "order-123", SKU: "SKU-1", Quantity: 1}
		assert.Equal(t, "AddItem", cmd.CommandType())
		assert.Equal(t, "order-123", cmd.AggregateID())
	})

	t.Run("IdempotentCommand interface", func(t *testing.T) {
		var cmd IdempotentCommand = TestIdempotentCommand{Key: "unique-key"}
		assert.Equal(t, "IdempotentCommand", cmd.CommandType())
		assert.Equal(t, "unique-key", cmd.IdempotencyKey())
	})
}

func TestCommandBase(t *testing.T) {
	t.Run("empty base", func(t *testing.T) {
		base := CommandBase{}
		assert.Empty(t, base.CommandID)
		assert.Empty(t, base.CorrelationID)
		assert.Empty(t, base.CausationID)
		assert.Nil(t, base.Metadata)
	})

	t.Run("WithCommandID", func(t *testing.T) {
		base := CommandBase{}.WithCommandID("cmd-123")
		assert.Equal(t, "cmd-123", base.CommandID)
	})

	t.Run("WithCorrelationID", func(t *testing.T) {
		base := CommandBase{}.WithCorrelationID("corr-123")
		assert.Equal(t, "corr-123", base.CorrelationID)
	})

	t.Run("WithCausationID", func(t *testing.T) {
		base := CommandBase{}.WithCausationID("cause-123")
		assert.Equal(t, "cause-123", base.CausationID)
	})

	t.Run("WithMetadata creates map", func(t *testing.T) {
		base := CommandBase{}.WithMetadata("key1", "value1")
		assert.Equal(t, "value1", base.Metadata["key1"])
	})

	t.Run("WithMetadata preserves existing", func(t *testing.T) {
		base := CommandBase{}.
			WithMetadata("key1", "value1").
			WithMetadata("key2", "value2")
		assert.Equal(t, "value1", base.Metadata["key1"])
		assert.Equal(t, "value2", base.Metadata["key2"])
	})

	t.Run("GetMetadata returns value", func(t *testing.T) {
		base := CommandBase{}.WithMetadata("key", "value")
		assert.Equal(t, "value", base.GetMetadata("key"))
	})

	t.Run("GetMetadata returns empty for missing", func(t *testing.T) {
		base := CommandBase{}
		assert.Empty(t, base.GetMetadata("missing"))
	})

	t.Run("chained builders", func(t *testing.T) {
		base := CommandBase{}.
			WithCommandID("cmd-1").
			WithCorrelationID("corr-1").
			WithCausationID("cause-1").
			WithMetadata("key", "value")

		assert.Equal(t, "cmd-1", base.CommandID)
		assert.Equal(t, "corr-1", base.CorrelationID)
		assert.Equal(t, "cause-1", base.CausationID)
		assert.Equal(t, "value", base.GetMetadata("key"))
	})
}

func TestCommandResult(t *testing.T) {
	t.Run("NewSuccessResult", func(t *testing.T) {
		result := NewSuccessResult("agg-123", 5)
		assert.True(t, result.Success)
		assert.Equal(t, "agg-123", result.AggregateID)
		assert.Equal(t, int64(5), result.Version)
		assert.Nil(t, result.Data)
		assert.NoError(t, result.Error)
		assert.True(t, result.IsSuccess())
		assert.False(t, result.IsError())
	})

	t.Run("NewSuccessResultWithData", func(t *testing.T) {
		data := map[string]string{"foo": "bar"}
		result := NewSuccessResultWithData("agg-123", 5, data)
		assert.True(t, result.Success)
		assert.Equal(t, "agg-123", result.AggregateID)
		assert.Equal(t, int64(5), result.Version)
		assert.Equal(t, data, result.Data)
		assert.True(t, result.IsSuccess())
	})

	t.Run("NewErrorResult", func(t *testing.T) {
		err := assert.AnError
		result := NewErrorResult(err)
		assert.False(t, result.Success)
		assert.Empty(t, result.AggregateID)
		assert.Equal(t, int64(0), result.Version)
		assert.Equal(t, err, result.Error)
		assert.False(t, result.IsSuccess())
		assert.True(t, result.IsError())
	})

	t.Run("IsError with success false but no error", func(t *testing.T) {
		result := CommandResult{Success: false}
		assert.True(t, result.IsError())
	})
}

func TestCommandContext(t *testing.T) {
	t.Run("NewCommandContext", func(t *testing.T) {
		ctx := context.Background()
		cmd := TestCreateOrder{CustomerID: "cust-123"}
		cmdCtx := NewCommandContext(ctx, cmd)

		assert.Equal(t, ctx, cmdCtx.Context)
		assert.Equal(t, cmd, cmdCtx.Command)
		assert.NotNil(t, cmdCtx.Metadata)
	})

	t.Run("Set and Get", func(t *testing.T) {
		cmdCtx := NewCommandContext(context.Background(), TestCreateOrder{})
		cmdCtx.Set("key", "value")

		v, ok := cmdCtx.Get("key")
		assert.True(t, ok)
		assert.Equal(t, "value", v)
	})

	t.Run("Get missing returns false", func(t *testing.T) {
		cmdCtx := NewCommandContext(context.Background(), TestCreateOrder{})
		_, ok := cmdCtx.Get("missing")
		assert.False(t, ok)
	})

	t.Run("GetString", func(t *testing.T) {
		cmdCtx := NewCommandContext(context.Background(), TestCreateOrder{})
		cmdCtx.Set("str", "hello")
		cmdCtx.Set("num", 123)

		assert.Equal(t, "hello", cmdCtx.GetString("str"))
		assert.Empty(t, cmdCtx.GetString("num")) // wrong type
		assert.Empty(t, cmdCtx.GetString("missing"))
	})

	t.Run("SetResult", func(t *testing.T) {
		cmdCtx := NewCommandContext(context.Background(), TestCreateOrder{})
		result := NewSuccessResult("agg-1", 1)
		cmdCtx.SetResult(result)

		assert.Equal(t, result, cmdCtx.Result)
	})

	t.Run("SetSuccess", func(t *testing.T) {
		cmdCtx := NewCommandContext(context.Background(), TestCreateOrder{})
		cmdCtx.SetSuccess("agg-1", 5)

		assert.True(t, cmdCtx.Result.Success)
		assert.Equal(t, "agg-1", cmdCtx.Result.AggregateID)
		assert.Equal(t, int64(5), cmdCtx.Result.Version)
	})

	t.Run("SetError", func(t *testing.T) {
		cmdCtx := NewCommandContext(context.Background(), TestCreateOrder{})
		cmdCtx.SetError(assert.AnError)

		assert.False(t, cmdCtx.Result.Success)
		assert.Equal(t, assert.AnError, cmdCtx.Result.Error)
	})
}

func TestValidationError(t *testing.T) {
	t.Run("Error message with field", func(t *testing.T) {
		err := NewValidationError("CreateOrder", "CustomerID", "required")
		assert.Contains(t, err.Error(), "CreateOrder")
		assert.Contains(t, err.Error(), "CustomerID")
		assert.Contains(t, err.Error(), "required")
	})

	t.Run("Error message without field", func(t *testing.T) {
		err := &ValidationError{CommandType: "CreateOrder", Message: "invalid"}
		assert.Contains(t, err.Error(), "CreateOrder")
		assert.Contains(t, err.Error(), "invalid")
		assert.NotContains(t, err.Error(), "field")
	})

	t.Run("Is ErrValidationFailed", func(t *testing.T) {
		err := NewValidationError("CreateOrder", "Field", "message")
		assert.ErrorIs(t, err, ErrValidationFailed)
	})

	t.Run("Unwrap returns cause", func(t *testing.T) {
		cause := assert.AnError
		err := NewValidationErrorWithCause("CreateOrder", "Field", "message", cause)
		assert.Equal(t, cause, err.Unwrap())
	})

	t.Run("Unwrap returns nil without cause", func(t *testing.T) {
		err := NewValidationError("CreateOrder", "Field", "message")
		assert.Nil(t, err.Unwrap())
	})
}

func TestMultiValidationError(t *testing.T) {
	t.Run("Error message", func(t *testing.T) {
		errs := NewMultiValidationError("CreateOrder")
		errs.AddField("Field1", "error 1")
		errs.AddField("Field2", "error 2")

		assert.Contains(t, errs.Error(), "CreateOrder")
		assert.Contains(t, errs.Error(), "2 error(s)")
	})

	t.Run("Is ErrValidationFailed", func(t *testing.T) {
		errs := NewMultiValidationError("CreateOrder")
		errs.AddField("Field", "error")
		assert.ErrorIs(t, errs, ErrValidationFailed)
	})

	t.Run("Unwrap returns first error", func(t *testing.T) {
		errs := NewMultiValidationError("CreateOrder")
		err1 := NewValidationError("CreateOrder", "Field1", "error 1")
		errs.Add(err1)
		errs.AddField("Field2", "error 2")

		assert.Equal(t, err1, errs.Unwrap())
	})

	t.Run("Unwrap returns nil when empty", func(t *testing.T) {
		errs := NewMultiValidationError("CreateOrder")
		assert.Nil(t, errs.Unwrap())
	})

	t.Run("HasErrors", func(t *testing.T) {
		errs := NewMultiValidationError("CreateOrder")
		assert.False(t, errs.HasErrors())

		errs.AddField("Field", "error")
		assert.True(t, errs.HasErrors())
	})
}

func TestCommand_Validation(t *testing.T) {
	t.Run("valid CreateOrder", func(t *testing.T) {
		cmd := TestCreateOrder{
			CustomerID: "cust-123",
			Items:      []TestCommandItem{{SKU: "SKU-1", Quantity: 1, Price: 10.0}},
		}
		assert.NoError(t, cmd.Validate())
	})

	t.Run("invalid CreateOrder - missing customer ID", func(t *testing.T) {
		cmd := TestCreateOrder{
			Items: []TestCommandItem{{SKU: "SKU-1", Quantity: 1, Price: 10.0}},
		}
		err := cmd.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)

		var validErr *ValidationError
		require.ErrorAs(t, err, &validErr)
		assert.Equal(t, "CustomerID", validErr.Field)
	})

	t.Run("invalid CreateOrder - no items", func(t *testing.T) {
		cmd := TestCreateOrder{
			CustomerID: "cust-123",
		}
		err := cmd.Validate()
		require.Error(t, err)

		var validErr *ValidationError
		require.ErrorAs(t, err, &validErr)
		assert.Equal(t, "Items", validErr.Field)
	})

	t.Run("valid AddItem", func(t *testing.T) {
		cmd := TestAddItem{
			OrderID:  "order-123",
			SKU:      "SKU-1",
			Quantity: 2,
			Price:    15.0,
		}
		assert.NoError(t, cmd.Validate())
	})

	t.Run("invalid AddItem - multiple errors", func(t *testing.T) {
		cmd := TestAddItem{
			Quantity: -1,
			Price:    -5.0,
		}
		err := cmd.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrValidationFailed)

		var multiErr *MultiValidationError
		require.ErrorAs(t, err, &multiErr)
		assert.GreaterOrEqual(t, len(multiErr.Errors), 3) // OrderID, SKU, Quantity at minimum
	})
}

func TestValidatorFunc(t *testing.T) {
	t.Run("implements Validator", func(t *testing.T) {
		var validator Validator = ValidatorFunc(func(cmd Command) error {
			if cmd.CommandType() == "CreateOrder" {
				return nil
			}
			return NewValidationError(cmd.CommandType(), "", "unknown command")
		})

		cmd1 := TestCreateOrder{CustomerID: "cust-1"}
		assert.NoError(t, validator.Validate(cmd1))

		cmd2 := TestAddItem{OrderID: "order-1", SKU: "SKU-1", Quantity: 1}
		assert.Error(t, validator.Validate(cmd2))
	})
}

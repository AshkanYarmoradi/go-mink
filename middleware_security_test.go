package mink

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recoverySensitiveCommand carries a field that must never leak into
// PanicError.CommandData unless capture is explicitly enabled.
type recoverySensitiveCommand struct {
	CommandBase
	OrderID    string `json:"orderId"`
	CardNumber string `json:"cardNumber"`
}

func (c recoverySensitiveCommand) CommandType() string { return "RecoverySensitiveCommand" }
func (c recoverySensitiveCommand) Validate() error     { return nil }
func (c recoverySensitiveCommand) AggregateID() string { return c.OrderID }

func panickingHandler(ctx context.Context, cmd Command) (CommandResult, error) {
	panic("boom")
}

func recoveredPanic(t *testing.T, mw Middleware, cmd Command) *PanicError {
	t.Helper()
	result, err := mw(panickingHandler)(context.Background(), cmd)
	require.Error(t, err)
	assert.True(t, result.IsError())
	var panicErr *PanicError
	require.ErrorAs(t, err, &panicErr)
	return panicErr
}

func TestRecoveryMiddleware_DefaultCommandDataOmitsCommandFields(t *testing.T) {
	cmd := recoverySensitiveCommand{OrderID: "order-1", CardNumber: "4111111111111111"}

	panicErr := recoveredPanic(t, RecoveryMiddleware(), cmd)

	assert.Equal(t, "RecoverySensitiveCommand", panicErr.CommandType)
	assert.Equal(t, `{"commandType":"RecoverySensitiveCommand","aggregateId":"order-1"}`, panicErr.CommandData)
	assert.NotContains(t, panicErr.CommandData, "4111", "sensitive command fields must not be captured by default")
	assert.NotContains(t, panicErr.CommandData, "cardNumber")
}

func TestRecoveryMiddleware_DefaultCommandData_NonAggregateCommand(t *testing.T) {
	cmd := middlewareTestCommand{Value: "secret-value"}

	panicErr := recoveredPanic(t, RecoveryMiddleware(), cmd)

	assert.Equal(t, `{"commandType":"TestCommand"}`, panicErr.CommandData)
	assert.NotContains(t, panicErr.CommandData, "secret-value")
}

func TestRecoveryMiddleware_DefaultCommandData_EmptyAggregateIDOmitted(t *testing.T) {
	cmd := recoverySensitiveCommand{OrderID: "", CardNumber: "4111111111111111"}

	panicErr := recoveredPanic(t, RecoveryMiddleware(), cmd)

	assert.Equal(t, `{"commandType":"RecoverySensitiveCommand"}`, panicErr.CommandData)
}

func TestRecoveryMiddleware_WithPanicCommandCapture_RecordsFullCommand(t *testing.T) {
	cmd := recoverySensitiveCommand{OrderID: "order-1", CardNumber: "4111111111111111"}

	panicErr := recoveredPanic(t, RecoveryMiddleware(WithPanicCommandCapture()), cmd)

	assert.Contains(t, panicErr.CommandData, `"cardNumber":"4111111111111111"`)
	assert.Contains(t, panicErr.CommandData, `"orderId":"order-1"`)
}

func TestRecoveryMiddleware_WithPanicCommandCapture_UnmarshalableCommand(t *testing.T) {
	panicErr := recoveredPanic(t, RecoveryMiddleware(WithPanicCommandCapture()), unmarshalableCommand{})

	assert.Equal(t, "", panicErr.CommandData, "an unserializable command yields no data rather than an error")
	assert.Equal(t, "UnmarshalableCommand", panicErr.CommandType)
}

func TestRecoveryMiddleware_WithOptions_StillPassesThroughSuccess(t *testing.T) {
	mw := RecoveryMiddleware(WithPanicCommandCapture())
	result, err := mw(func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewSuccessResult("agg-1", 1), nil
	})(context.Background(), middlewareTestCommand{Value: "ok"})

	require.NoError(t, err)
	assert.True(t, result.IsSuccess())
}

func TestPanicCommandData(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
		full bool
		want string
	}{
		{
			name: "summary for aggregate command",
			cmd:  recoverySensitiveCommand{OrderID: "o-1", CardNumber: "x"},
			want: `{"commandType":"RecoverySensitiveCommand","aggregateId":"o-1"}`,
		},
		{
			name: "summary for plain command",
			cmd:  middlewareTestCommand{Value: "v"},
			want: `{"commandType":"TestCommand"}`,
		},
		{
			name: "full capture serializes the command",
			cmd:  middlewareTestCommand{Value: "v"},
			full: true,
			want: `{"Value":"v","Fail":false}`,
		},
		{
			name: "full capture of unserializable command is empty",
			cmd:  unmarshalableCommand{},
			full: true,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, panicCommandData(tt.cmd, tt.full))
		})
	}
}

func TestCorrelationIDMiddleware_DefaultGeneratorIsRandomUUID(t *testing.T) {
	mw := CorrelationIDMiddleware(nil)

	var ids []string
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		ids = append(ids, CorrelationIDFromContext(ctx))
		return NewSuccessResult("", 0), nil
	}
	for i := 0; i < 3; i++ {
		_, err := mw(handler)(context.Background(), middlewareTestCommand{Value: "test"})
		require.NoError(t, err)
	}

	require.Len(t, ids, 3)
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		parsed, err := uuid.Parse(id)
		require.NoError(t, err, "default correlation ID %q must be a UUID", id)
		assert.Equal(t, uuid.Version(4), parsed.Version(), "default correlation ID must be a random (v4) UUID")
		assert.False(t, seen[id], "correlation IDs must not collide: %q", id)
		seen[id] = true
	}
}

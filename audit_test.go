package mink

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
)

// --- Test fixtures ---------------------------------------------------------

// auditTestCommand is a plain command (no aggregate ID) with metadata support
// via the embedded CommandBase.
type auditTestCommand struct {
	CommandBase
	Type string
}

func (c auditTestCommand) CommandType() string { return c.Type }
func (c auditTestCommand) Validate() error     { return nil }

// auditAggregateCommand targets a specific aggregate.
type auditAggregateCommand struct {
	CommandBase
	ID string
}

func (c auditAggregateCommand) CommandType() string { return "AggCmd" }
func (c auditAggregateCommand) Validate() error     { return nil }
func (c auditAggregateCommand) AggregateID() string { return c.ID }

// failingAuditStore wraps a real store but fails Append on demand.
type failingAuditStore struct {
	*memory.AuditStore
	appendErr error
}

func (s *failingAuditStore) Append(ctx context.Context, entry *adapters.AuditEntry) error {
	if s.appendErr != nil {
		return s.appendErr
	}
	return s.AuditStore.Append(ctx, entry)
}

// fixedConfig returns a config with a deterministic clock and ID generator.
//
// AuditMiddleware calls now() three times per command, in order:
//  1. start (captured before the handler runs)
//  2. entry.Timestamp (after the handler returns)
//  3. the end time used to compute DurationMs
//
// The clock below returns base, base+1500ms, base+1500ms, so Timestamp is
// base+1500ms and DurationMs is 1500.
func fixedConfig(store AuditStore) AuditConfig {
	cfg := DefaultAuditConfig(store)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	times := []time.Time{
		base,
		base.Add(1500 * time.Millisecond),
		base.Add(1500 * time.Millisecond),
	}
	calls := 0
	cfg.now = func() time.Time {
		t := times[calls%len(times)]
		calls++
		return t
	}
	cfg.idgen = func() string { return "fixed-id" }
	return cfg
}

func okHandler(aggID string, version int64) MiddlewareFunc {
	return func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewSuccessResult(aggID, version), nil
	}
}

// --- Tests -----------------------------------------------------------------

func TestAuditMiddleware_RecordsSuccess(t *testing.T) {
	store := memory.NewAuditStore()
	cfg := fixedConfig(store)
	mw := AuditMiddleware(cfg)

	ctx := WithActor(context.Background(), "alice")
	cmd := auditTestCommand{
		CommandBase: CommandBase{CommandID: "cmd-1"},
		Type:        "DoThing",
	}
	result, err := mw(okHandler("agg-7", 3))(ctx, cmd)
	require.NoError(t, err)
	assert.True(t, result.IsSuccess())

	entries, err := store.Find(ctx, AuditQuery{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	e := entries[0]
	assert.Equal(t, "fixed-id", e.ID)
	assert.Equal(t, "DoThing", e.CommandType)
	assert.Equal(t, "cmd-1", e.CommandID)
	assert.Equal(t, "agg-7", e.AggregateID)
	assert.Equal(t, int64(3), e.Version)
	assert.Equal(t, "alice", e.Actor)
	assert.True(t, e.Success)
	assert.Empty(t, e.Error)
	assert.Equal(t, int64(1500), e.DurationMs)
	assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 1, 500000000, time.UTC), e.Timestamp)
}

func TestAuditMiddleware_RecordsHandlerError(t *testing.T) {
	store := memory.NewAuditStore()
	mw := AuditMiddleware(DefaultAuditConfig(store))

	wantErr := errors.New("handler boom")
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewErrorResult(wantErr), wantErr
	}
	_, err := mw(handler)(context.Background(), auditTestCommand{Type: "DoThing"})
	require.ErrorIs(t, err, wantErr)

	entries, err := store.Find(context.Background(), AuditQuery{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.False(t, entries[0].Success)
	assert.Equal(t, "handler boom", entries[0].Error)
}

func TestAuditMiddleware_RecordsErrorResultWithoutErr(t *testing.T) {
	// A handler may return a failure CommandResult with a nil error.
	store := memory.NewAuditStore()
	mw := AuditMiddleware(DefaultAuditConfig(store))

	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewErrorResult(errors.New("result-level failure")), nil
	}
	_, err := mw(handler)(context.Background(), auditTestCommand{Type: "DoThing"})
	require.NoError(t, err)

	entries, err := store.Find(context.Background(), AuditQuery{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.False(t, entries[0].Success)
	assert.Equal(t, "result-level failure", entries[0].Error)
}

func TestAuditMiddleware_SkipCommands(t *testing.T) {
	store := memory.NewAuditStore()
	cfg := DefaultAuditConfig(store)
	cfg.SkipCommands = []string{"SkipMe"}
	mw := AuditMiddleware(cfg)

	_, err := mw(okHandler("a", 1))(context.Background(), auditTestCommand{Type: "SkipMe"})
	require.NoError(t, err)
	_, err = mw(okHandler("a", 1))(context.Background(), auditTestCommand{Type: "AuditMe"})
	require.NoError(t, err)

	assert.Equal(t, 1, store.Len())
	entries, err := store.Find(context.Background(), AuditQuery{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "AuditMe", entries[0].CommandType)
}

func TestAuditMiddleware_ActorResolution(t *testing.T) {
	t.Run("default reads from context", func(t *testing.T) {
		store := memory.NewAuditStore()
		mw := AuditMiddleware(DefaultAuditConfig(store))
		ctx := WithActor(context.Background(), "ctx-actor")
		_, err := mw(okHandler("a", 1))(ctx, auditTestCommand{Type: "C"})
		require.NoError(t, err)
		entries, _ := store.Find(ctx, AuditQuery{})
		require.Len(t, entries, 1)
		assert.Equal(t, "ctx-actor", entries[0].Actor)
	})

	t.Run("default empty when not set", func(t *testing.T) {
		store := memory.NewAuditStore()
		mw := AuditMiddleware(DefaultAuditConfig(store))
		_, err := mw(okHandler("a", 1))(context.Background(), auditTestCommand{Type: "C"})
		require.NoError(t, err)
		entries, _ := store.Find(context.Background(), AuditQuery{})
		require.Len(t, entries, 1)
		assert.Empty(t, entries[0].Actor)
	})

	t.Run("custom ActorFunc", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := DefaultAuditConfig(store)
		cfg.ActorFunc = func(ctx context.Context, cmd Command) string {
			return "custom:" + cmd.CommandType()
		}
		mw := AuditMiddleware(cfg)
		_, err := mw(okHandler("a", 1))(context.Background(), auditTestCommand{Type: "C"})
		require.NoError(t, err)
		entries, _ := store.Find(context.Background(), AuditQuery{})
		require.Len(t, entries, 1)
		assert.Equal(t, "custom:C", entries[0].Actor)
	})

	t.Run("nil ActorFunc is normalized to default", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := AuditConfig{Store: store, ActorFunc: nil}
		mw := AuditMiddleware(cfg)
		ctx := WithActor(context.Background(), "fallback")
		_, err := mw(okHandler("a", 1))(ctx, auditTestCommand{Type: "C"})
		require.NoError(t, err)
		entries, _ := store.Find(ctx, AuditQuery{})
		require.Len(t, entries, 1)
		assert.Equal(t, "fallback", entries[0].Actor)
	})
}

func TestAuditMiddleware_AggregateIDFromCommand(t *testing.T) {
	store := memory.NewAuditStore()
	mw := AuditMiddleware(DefaultAuditConfig(store))

	// Handler returns a success result with no aggregate ID; the middleware
	// should fall back to the command's AggregateID().
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewSuccessResult("", 0), nil
	}
	_, err := mw(handler)(context.Background(), auditAggregateCommand{ID: "agg-from-cmd"})
	require.NoError(t, err)

	entries, _ := store.Find(context.Background(), AuditQuery{})
	require.Len(t, entries, 1)
	assert.Equal(t, "agg-from-cmd", entries[0].AggregateID)
}

func TestAuditMiddleware_ResultAggregateIDWins(t *testing.T) {
	store := memory.NewAuditStore()
	mw := AuditMiddleware(DefaultAuditConfig(store))

	_, err := mw(okHandler("agg-from-result", 2))(context.Background(),
		auditAggregateCommand{ID: "agg-from-cmd"})
	require.NoError(t, err)

	entries, _ := store.Find(context.Background(), AuditQuery{})
	require.Len(t, entries, 1)
	assert.Equal(t, "agg-from-result", entries[0].AggregateID)
}

func TestAuditMiddleware_CapturesContextIDs(t *testing.T) {
	store := memory.NewAuditStore()
	// Chain correlation middleware (which seeds the correlation ID into context)
	// ahead of the audit middleware to verify the audit captures it.
	chain := ChainMiddleware(
		CorrelationIDMiddleware(func() string { return "corr-9" }),
		AuditMiddleware(DefaultAuditConfig(store)),
	)

	ctx := context.Background()
	ctx = WithTenantID(ctx, "tenant-9")
	ctx = WithCausationID(ctx, "cause-9")

	_, err := chain(okHandler("a", 1))(ctx, auditTestCommand{Type: "C"})
	require.NoError(t, err)

	entries, _ := store.Find(ctx, AuditQuery{})
	require.Len(t, entries, 1)
	assert.Equal(t, "tenant-9", entries[0].TenantID)
	assert.Equal(t, "corr-9", entries[0].CorrelationID)
	assert.Equal(t, "cause-9", entries[0].CausationID)
}

func TestAuditMiddleware_IncludeMetadata(t *testing.T) {
	t.Run("enabled copies command metadata", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := DefaultAuditConfig(store)
		cfg.IncludeMetadata = true
		mw := AuditMiddleware(cfg)

		cmd := auditTestCommand{Type: "C"}
		cmd.CommandBase = cmd.WithMetadata("ip", "10.0.0.1").WithMetadata("ua", "curl")

		_, err := mw(okHandler("a", 1))(context.Background(), cmd)
		require.NoError(t, err)

		entries, _ := store.Find(context.Background(), AuditQuery{})
		require.Len(t, entries, 1)
		assert.Equal(t, map[string]string{"ip": "10.0.0.1", "ua": "curl"}, entries[0].Metadata)
	})

	t.Run("enabled with no command metadata yields nil", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := DefaultAuditConfig(store)
		cfg.IncludeMetadata = true
		mw := AuditMiddleware(cfg)

		// Command has the GetMetadataMap method but an empty/nil map.
		_, err := mw(okHandler("a", 1))(context.Background(), auditTestCommand{Type: "C"})
		require.NoError(t, err)

		entries, _ := store.Find(context.Background(), AuditQuery{})
		require.Len(t, entries, 1)
		assert.Nil(t, entries[0].Metadata)
	})

	t.Run("disabled omits metadata", func(t *testing.T) {
		store := memory.NewAuditStore()
		mw := AuditMiddleware(DefaultAuditConfig(store)) // IncludeMetadata defaults false

		cmd := auditTestCommand{Type: "C"}
		cmd.CommandBase = cmd.WithMetadata("ip", "10.0.0.1")

		_, err := mw(okHandler("a", 1))(context.Background(), cmd)
		require.NoError(t, err)

		entries, _ := store.Find(context.Background(), AuditQuery{})
		require.Len(t, entries, 1)
		assert.Nil(t, entries[0].Metadata)
	})
}

func TestAuditMiddleware_FailOpen(t *testing.T) {
	// Store write fails but the original successful result must be returned.
	store := &failingAuditStore{AuditStore: memory.NewAuditStore(), appendErr: errors.New("store down")}
	mw := AuditMiddleware(DefaultAuditConfig(store))

	result, err := mw(okHandler("agg-1", 1))(context.Background(), auditTestCommand{Type: "C"})
	require.NoError(t, err)
	assert.True(t, result.IsSuccess())
	assert.Equal(t, "agg-1", result.AggregateID)
}

func TestAuditMiddleware_FailOpen_PreservesHandlerError(t *testing.T) {
	store := &failingAuditStore{AuditStore: memory.NewAuditStore(), appendErr: errors.New("store down")}
	mw := AuditMiddleware(DefaultAuditConfig(store))

	handlerErr := errors.New("handler failed")
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewErrorResult(handlerErr), handlerErr
	}
	_, err := mw(handler)(context.Background(), auditTestCommand{Type: "C"})
	// Fail-open: the audit write error is ignored; the original handler error survives.
	require.ErrorIs(t, err, handlerErr)
}

func TestAuditMiddleware_FailClosed(t *testing.T) {
	appendErr := errors.New("store down")
	store := &failingAuditStore{AuditStore: memory.NewAuditStore(), appendErr: appendErr}
	cfg := DefaultAuditConfig(store)
	cfg.FailClosed = true
	mw := AuditMiddleware(cfg)

	result, err := mw(okHandler("agg-1", 1))(context.Background(), auditTestCommand{Type: "C"})
	require.ErrorIs(t, err, appendErr)
	assert.True(t, result.IsError())
}

func TestAuditMiddleware_FailClosed_PreservesHandlerError(t *testing.T) {
	appendErr := errors.New("store down")
	store := &failingAuditStore{AuditStore: memory.NewAuditStore(), appendErr: appendErr}
	cfg := DefaultAuditConfig(store)
	cfg.FailClosed = true
	mw := AuditMiddleware(cfg)

	handlerErr := errors.New("handler failed")
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewErrorResult(handlerErr), handlerErr
	}
	_, err := mw(handler)(context.Background(), auditTestCommand{Type: "C"})
	// FailClosed surfaces the audit error but must not mask the handler error.
	require.ErrorIs(t, err, handlerErr)
	require.ErrorIs(t, err, appendErr)
}

func TestAuditMiddleware_FailClosed_PreservesErrorResult(t *testing.T) {
	appendErr := errors.New("store down")
	store := &failingAuditStore{AuditStore: memory.NewAuditStore(), appendErr: appendErr}
	cfg := DefaultAuditConfig(store)
	cfg.FailClosed = true
	mw := AuditMiddleware(cfg)

	resultErr := errors.New("result-level failure")
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		return NewErrorResult(resultErr), nil // error result, but a nil Go error
	}
	result, err := mw(handler)(context.Background(), auditTestCommand{Type: "C"})
	// The command's error result must be preserved, not masked by the audit error.
	require.True(t, result.IsError())
	require.ErrorIs(t, err, resultErr)
	require.ErrorIs(t, err, appendErr)
}

func TestAuditMiddleware_PanicDownstreamStillAudited(t *testing.T) {
	// The audit middleware wraps the recovery middleware, so when a downstream
	// handler panics, recovery converts it into an error result *before* the
	// audit middleware records the outcome.
	store := memory.NewAuditStore()
	chain := ChainMiddleware(
		AuditMiddleware(DefaultAuditConfig(store)),
		RecoveryMiddleware(),
	)
	handler := func(ctx context.Context, cmd Command) (CommandResult, error) {
		panic("kaboom")
	}
	_, err := chain(handler)(context.Background(), auditTestCommand{Type: "Panicky"})
	require.Error(t, err)

	entries, ferr := store.Find(context.Background(), AuditQuery{})
	require.NoError(t, ferr)
	require.Len(t, entries, 1)
	assert.Equal(t, "Panicky", entries[0].CommandType)
	assert.False(t, entries[0].Success)
	assert.NotEmpty(t, entries[0].Error)
}

func TestDefaultAuditConfig(t *testing.T) {
	store := memory.NewAuditStore()
	cfg := DefaultAuditConfig(store)
	assert.Equal(t, store, cfg.Store)
	assert.NotNil(t, cfg.ActorFunc)
	assert.False(t, cfg.FailClosed)
	assert.False(t, cfg.IncludeMetadata)
	assert.Nil(t, cfg.SkipCommands)
}

func TestActorContextHelpers(t *testing.T) {
	assert.Empty(t, ActorFromContext(context.Background()))
	ctx := WithActor(context.Background(), "u-1")
	assert.Equal(t, "u-1", ActorFromContext(ctx))
}

func TestNewAuditID_IsUUIDv4(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := make(map[string]struct{})
	for i := 0; i < 1000; i++ {
		id := newAuditID()
		assert.Regexp(t, re, id)
		_, dup := seen[id]
		assert.False(t, dup, "generated duplicate UUID: %s", id)
		seen[id] = struct{}{}
	}
}

func TestAuditOrderReexports(t *testing.T) {
	assert.Equal(t, adapters.AuditOrderTimestampDesc, AuditOrderTimestampDesc)
	assert.Equal(t, adapters.AuditOrderTimestampAsc, AuditOrderTimestampAsc)
}

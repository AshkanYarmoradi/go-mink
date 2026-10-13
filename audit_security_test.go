package mink

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
)

// auditOverlongCommand lets a test control every client-supplied value that
// lands in a bounded audit column.
type auditOverlongCommand struct {
	CommandBase
	Type string
	ID   string
}

func (c auditOverlongCommand) CommandType() string { return c.Type }
func (c auditOverlongCommand) Validate() error     { return nil }
func (c auditOverlongCommand) AggregateID() string { return c.ID }

// captureSlogDefault routes log/slog's default logger into a buffer for the
// duration of the test and restores the previous default afterwards.
func captureSlogDefault(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

const auditDropWarning = "mink: audit entry dropped: store write failed (fail-open)"

// --- Fail-open visibility ---------------------------------------------------

func TestAuditMiddleware_FailOpen_LogsWarning(t *testing.T) {
	store := &failingAuditStore{AuditStore: memory.NewAuditStore(), appendErr: errors.New("store down")}
	logger := newTestLogger()
	cfg := DefaultAuditConfig(store)
	cfg.Logger = logger
	mw := AuditMiddleware(cfg)

	result, err := mw(okHandler("agg-1", 1))(context.Background(), auditTestCommand{Type: "C"})

	require.NoError(t, err)
	assert.True(t, result.IsSuccess(), "fail-open keeps the command outcome")
	assert.True(t, logger.hasLogMessage("warn", auditDropWarning), "a dropped audit entry must be logged")
}

func TestAuditMiddleware_FailOpen_DefaultLoggerIsVisible(t *testing.T) {
	buf := captureSlogDefault(t, slog.LevelInfo)
	store := &failingAuditStore{AuditStore: memory.NewAuditStore(), appendErr: errors.New("store down")}
	mw := AuditMiddleware(DefaultAuditConfig(store)) // Logger nil -> slog default

	ctx := WithTenantID(context.Background(), "tenant-9")
	cmd := auditTestCommand{CommandBase: CommandBase{CommandID: "cmd-1"}, Type: "C"}
	_, err := mw(okHandler("agg-1", 1))(ctx, cmd)

	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "level=WARN")
	assert.Contains(t, out, "audit entry dropped")
	assert.Contains(t, out, "commandType=C")
	assert.Contains(t, out, "commandId=cmd-1")
	assert.Contains(t, out, "tenantId=tenant-9")
	assert.Contains(t, out, `error="store down"`)
}

func TestAuditMiddleware_FailOpen_SuccessfulWriteDoesNotWarn(t *testing.T) {
	logger := newTestLogger()
	cfg := DefaultAuditConfig(memory.NewAuditStore())
	cfg.Logger = logger
	mw := AuditMiddleware(cfg)

	_, err := mw(okHandler("agg-1", 1))(context.Background(), auditTestCommand{Type: "C"})

	require.NoError(t, err)
	assert.Empty(t, logger.warnLogs)
}

func TestAuditMiddleware_FailClosed_DoesNotLogFailOpenWarning(t *testing.T) {
	appendErr := errors.New("store down")
	store := &failingAuditStore{AuditStore: memory.NewAuditStore(), appendErr: appendErr}
	logger := newTestLogger()
	cfg := DefaultAuditConfig(store)
	cfg.Logger = logger
	cfg.FailClosed = true
	mw := AuditMiddleware(cfg)

	_, err := mw(okHandler("agg-1", 1))(context.Background(), auditTestCommand{Type: "C"})

	require.ErrorIs(t, err, appendErr, "fail-closed surfaces the error to the caller instead")
	assert.False(t, logger.hasLogMessage("warn", auditDropWarning))
}

func TestAuditMiddleware_NilStore_FailOpen_WarnsOnceAtConstruction(t *testing.T) {
	logger := newTestLogger()
	mw := AuditMiddleware(AuditConfig{Logger: logger})

	const msg = "mink: audit middleware configured without a store; commands will not be audited"
	assert.True(t, logger.hasLogMessage("warn", msg))

	wrapped := mw(okHandler("agg-1", 1))
	for i := 0; i < 3; i++ {
		result, err := wrapped(context.Background(), auditTestCommand{Type: "C"})
		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
	}
	assert.Len(t, logger.warnLogs, 1, "the warning is emitted once per AuditMiddleware call, not per dispatch")
}

func TestAuditMiddleware_NilStore_FailClosed_DoesNotWarn(t *testing.T) {
	logger := newTestLogger()
	_ = AuditMiddleware(AuditConfig{Logger: logger, FailClosed: true})
	assert.Empty(t, logger.warnLogs, "fail-closed surfaces ErrNilAuditStore per command instead of warning")
}

// --- OnError hook -----------------------------------------------------------

func TestAuditMiddleware_OnError_InvokedInBothModes(t *testing.T) {
	for _, failClosed := range []bool{false, true} {
		name := "fail-open"
		if failClosed {
			name = "fail-closed"
		}
		t.Run(name, func(t *testing.T) {
			appendErr := errors.New("store down")
			store := &failingAuditStore{AuditStore: memory.NewAuditStore(), appendErr: appendErr}
			cfg := DefaultAuditConfig(store)
			cfg.Logger = newTestLogger()
			cfg.FailClosed = failClosed

			var gotEntry *AuditEntry
			var gotErr error
			calls := 0
			cfg.OnError = func(ctx context.Context, entry *AuditEntry, err error) {
				calls++
				gotEntry = entry
				gotErr = err
			}
			mw := AuditMiddleware(cfg)

			ctx := WithActor(context.Background(), "alice")
			_, err := mw(okHandler("agg-1", 1))(ctx, auditTestCommand{Type: "C"})
			if failClosed {
				require.ErrorIs(t, err, appendErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, 1, calls)
			require.NotNil(t, gotEntry)
			assert.Equal(t, "C", gotEntry.CommandType)
			assert.Equal(t, "alice", gotEntry.Actor)
			assert.Equal(t, "agg-1", gotEntry.AggregateID)
			assert.ErrorIs(t, gotErr, appendErr)
		})
	}
}

func TestAuditMiddleware_OnError_NotInvokedOnSuccessfulWrite(t *testing.T) {
	cfg := DefaultAuditConfig(memory.NewAuditStore())
	calls := 0
	cfg.OnError = func(context.Context, *AuditEntry, error) { calls++ }
	mw := AuditMiddleware(cfg)

	_, err := mw(okHandler("agg-1", 1))(context.Background(), auditTestCommand{Type: "C"})

	require.NoError(t, err)
	assert.Zero(t, calls)
}

// --- Field bounds -----------------------------------------------------------

func TestAuditMiddleware_TruncatesBoundedFields(t *testing.T) {
	long := strings.Repeat("x", 1000)
	store := memory.NewAuditStore()
	cfg := DefaultAuditConfig(store)
	cfg.ActorFunc = func(context.Context, Command) string { return long }
	mw := AuditMiddleware(cfg)

	ctx := WithTenantID(context.Background(), long)
	ctx = context.WithValue(ctx, correlationIDKey{}, long)
	ctx = WithCausationID(ctx, long)
	cmd := auditOverlongCommand{CommandBase: CommandBase{CommandID: long}, Type: long, ID: long}

	// A result without an aggregate ID makes the middleware fall back to the
	// command's (over-long) aggregate ID.
	result, err := mw(okHandler("", 0))(ctx, cmd)
	require.NoError(t, err)
	assert.True(t, result.IsSuccess(), "an over-long value must not fail the command")

	entries, err := store.Find(ctx, AuditQuery{})
	require.NoError(t, err)
	require.Len(t, entries, 1, "the row must be written, not dropped")
	e := entries[0]

	want := long[:MaxAuditFieldLength]
	assert.Equal(t, want, e.CommandType)
	assert.Equal(t, want, e.CommandID)
	assert.Equal(t, want, e.AggregateID)
	assert.Equal(t, want, e.Actor)
	assert.Equal(t, want, e.TenantID)
	assert.Equal(t, want, e.CorrelationID)
	assert.Equal(t, want, e.CausationID)
}

func TestAuditMiddleware_TruncatesResultAggregateID(t *testing.T) {
	long := strings.Repeat("a", 300)
	store := memory.NewAuditStore()
	mw := AuditMiddleware(DefaultAuditConfig(store))

	_, err := mw(okHandler(long, 1))(context.Background(), auditTestCommand{Type: "C"})
	require.NoError(t, err)

	entries, _ := store.Find(context.Background(), AuditQuery{})
	require.Len(t, entries, 1)
	assert.Len(t, entries[0].AggregateID, MaxAuditFieldLength)
}

func TestAuditMiddleware_Truncation_RespectsRuneBoundary(t *testing.T) {
	actor := strings.Repeat("é", 200) // 400 bytes; byte 255 falls inside a rune
	store := memory.NewAuditStore()
	mw := AuditMiddleware(DefaultAuditConfig(store))

	_, err := mw(okHandler("agg-1", 1))(WithActor(context.Background(), actor), auditTestCommand{Type: "C"})
	require.NoError(t, err)

	entries, _ := store.Find(context.Background(), AuditQuery{})
	require.Len(t, entries, 1)
	got := entries[0].Actor
	assert.True(t, utf8.ValidString(got), "truncation must not split a multi-byte rune")
	assert.LessOrEqual(t, len(got), MaxAuditFieldLength)
	assert.Equal(t, MaxAuditFieldLength-1, len(got), "254 bytes = 127 whole runes")
	assert.True(t, strings.HasPrefix(actor, got))
}

func TestAuditMiddleware_ShortFieldsUnchanged(t *testing.T) {
	store := memory.NewAuditStore()
	mw := AuditMiddleware(DefaultAuditConfig(store))

	ctx := WithActor(WithTenantID(context.Background(), "t-1"), "alice")
	cmd := auditOverlongCommand{CommandBase: CommandBase{CommandID: "cmd-1"}, Type: "Short", ID: "agg-1"}
	_, err := mw(okHandler("", 0))(ctx, cmd)
	require.NoError(t, err)

	entries, _ := store.Find(ctx, AuditQuery{})
	require.Len(t, entries, 1)
	assert.Equal(t, "Short", entries[0].CommandType)
	assert.Equal(t, "cmd-1", entries[0].CommandID)
	assert.Equal(t, "agg-1", entries[0].AggregateID)
	assert.Equal(t, "alice", entries[0].Actor)
	assert.Equal(t, "t-1", entries[0].TenantID)
}

func TestTruncateUTF8Bytes(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxBytes int
		want     string
	}{
		{name: "shorter than max is unchanged", in: "abc", maxBytes: 10, want: "abc"},
		{name: "exactly max is unchanged", in: "abcde", maxBytes: 5, want: "abcde"},
		{name: "ascii is cut at max", in: "abcdef", maxBytes: 3, want: "abc"},
		{name: "empty stays empty", in: "", maxBytes: 3, want: ""},
		{name: "cut on a rune boundary", in: "ééé", maxBytes: 3, want: "é"},
		{name: "cut exactly between runes", in: "ééé", maxBytes: 4, want: "éé"},
		{name: "4-byte rune is not split", in: "a😀b", maxBytes: 3, want: "a"},
		{name: "zero max yields empty", in: "abc", maxBytes: 0, want: ""},
		{name: "negative max yields empty", in: "abc", maxBytes: -1, want: ""},
		{name: "max smaller than first rune yields empty", in: "😀", maxBytes: 2, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateUTF8Bytes(tt.in, tt.maxBytes)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got))
		})
	}
}

// --- Metadata filter --------------------------------------------------------

func TestAuditMiddleware_MetadataFilter(t *testing.T) {
	newCmd := func() auditTestCommand {
		cmd := auditTestCommand{Type: "C"}
		cmd.CommandBase = cmd.WithMetadata("ip", "10.0.0.1").WithMetadata("email", "alice@example.com")
		return cmd
	}

	t.Run("drops keys", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := DefaultAuditConfig(store)
		cfg.IncludeMetadata = true
		cfg.MetadataFilter = func(m map[string]string) map[string]string {
			delete(m, "email")
			return m
		}
		mw := AuditMiddleware(cfg)

		_, err := mw(okHandler("a", 1))(context.Background(), newCmd())
		require.NoError(t, err)

		entries, _ := store.Find(context.Background(), AuditQuery{})
		require.Len(t, entries, 1)
		assert.Equal(t, map[string]string{"ip": "10.0.0.1"}, entries[0].Metadata)
	})

	t.Run("masks values", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := DefaultAuditConfig(store)
		cfg.IncludeMetadata = true
		cfg.MetadataFilter = func(m map[string]string) map[string]string {
			return map[string]string{"email": "[redacted]"}
		}
		mw := AuditMiddleware(cfg)

		_, err := mw(okHandler("a", 1))(context.Background(), newCmd())
		require.NoError(t, err)

		entries, _ := store.Find(context.Background(), AuditQuery{})
		require.Len(t, entries, 1)
		assert.Equal(t, map[string]string{"email": "[redacted]"}, entries[0].Metadata)
	})

	t.Run("returning nil omits metadata", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := DefaultAuditConfig(store)
		cfg.IncludeMetadata = true
		cfg.MetadataFilter = func(map[string]string) map[string]string { return nil }
		mw := AuditMiddleware(cfg)

		_, err := mw(okHandler("a", 1))(context.Background(), newCmd())
		require.NoError(t, err)

		entries, _ := store.Find(context.Background(), AuditQuery{})
		require.Len(t, entries, 1)
		assert.Nil(t, entries[0].Metadata)
	})

	t.Run("returning an empty map omits metadata", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := DefaultAuditConfig(store)
		cfg.IncludeMetadata = true
		cfg.MetadataFilter = func(map[string]string) map[string]string { return map[string]string{} }
		mw := AuditMiddleware(cfg)

		_, err := mw(okHandler("a", 1))(context.Background(), newCmd())
		require.NoError(t, err)

		entries, _ := store.Find(context.Background(), AuditQuery{})
		require.Len(t, entries, 1)
		assert.Nil(t, entries[0].Metadata)
	})

	t.Run("not called when the command has no metadata", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := DefaultAuditConfig(store)
		cfg.IncludeMetadata = true
		calls := 0
		cfg.MetadataFilter = func(m map[string]string) map[string]string { calls++; return m }
		mw := AuditMiddleware(cfg)

		_, err := mw(okHandler("a", 1))(context.Background(), auditTestCommand{Type: "C"})
		require.NoError(t, err)
		assert.Zero(t, calls)
	})

	t.Run("ignored when IncludeMetadata is off", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := DefaultAuditConfig(store)
		calls := 0
		cfg.MetadataFilter = func(m map[string]string) map[string]string { calls++; return m }
		mw := AuditMiddleware(cfg)

		_, err := mw(okHandler("a", 1))(context.Background(), newCmd())
		require.NoError(t, err)

		entries, _ := store.Find(context.Background(), AuditQuery{})
		require.Len(t, entries, 1)
		assert.Nil(t, entries[0].Metadata)
		assert.Zero(t, calls)
	})

	t.Run("receives a private copy", func(t *testing.T) {
		store := memory.NewAuditStore()
		cfg := DefaultAuditConfig(store)
		cfg.IncludeMetadata = true
		cfg.MetadataFilter = func(m map[string]string) map[string]string {
			m["ip"] = "mutated"
			return m
		}
		mw := AuditMiddleware(cfg)

		cmd := newCmd()
		_, err := mw(okHandler("a", 1))(context.Background(), cmd)
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.1", cmd.GetMetadata("ip"), "the filter must not reach the command's own map")
	})
}

// --- Default logger adapter -------------------------------------------------

func TestSlogDefaultLogger_ForwardsAllLevels(t *testing.T) {
	buf := captureSlogDefault(t, slog.LevelDebug)
	var l Logger = slogDefaultLogger{}

	l.Debug("dbg-msg", "k", "v")
	l.Info("info-msg")
	l.Warn("warn-msg")
	l.Error("err-msg")

	out := buf.String()
	assert.Contains(t, out, "level=DEBUG msg=dbg-msg k=v")
	assert.Contains(t, out, "level=INFO msg=info-msg")
	assert.Contains(t, out, "level=WARN msg=warn-msg")
	assert.Contains(t, out, "level=ERROR msg=err-msg")
}

func TestDefaultAuditLogger_IsSlogBacked(t *testing.T) {
	_, ok := defaultAuditLogger().(slogDefaultLogger)
	assert.True(t, ok)
}

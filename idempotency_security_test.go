package mink

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emptyKeyIdempotentCommand implements IdempotentCommand but never sets a key:
// the misuse that used to collapse every such command onto one global key.
type emptyKeyIdempotentCommand struct {
	CommandBase
	Value string
}

func (c emptyKeyIdempotentCommand) CommandType() string    { return "EmptyKeyIdempotentCommand" }
func (c emptyKeyIdempotentCommand) Validate() error        { return nil }
func (c emptyKeyIdempotentCommand) IdempotencyKey() string { return "" }

// countingHandler returns a handler that counts invocations and reports the
// tenant it ran for in the aggregate ID.
func countingHandler(calls *int) MiddlewareFunc {
	return func(ctx context.Context, cmd Command) (CommandResult, error) {
		*calls++
		return NewSuccessResult("agg-"+TenantIDFromContext(ctx), int64(*calls)), nil
	}
}

// --- Empty client key -------------------------------------------------------

func TestGetIdempotencyKey_EmptyClientKey_FallsBackToContentHash(t *testing.T) {
	a := emptyKeyIdempotentCommand{Value: "a"}
	b := emptyKeyIdempotentCommand{Value: "b"}

	keyA := GetIdempotencyKey(a)
	keyB := GetIdempotencyKey(b)

	require.NotEmpty(t, keyA)
	assert.Equal(t, GenerateIdempotencyKey(a), keyA, "an empty client key must fall back to the content hash")
	assert.NotEqual(t, keyA, keyB, "distinct commands with unset keys must not share a key")
	assert.Equal(t, keyA, GetIdempotencyKey(a), "the fallback must be deterministic")
}

func TestIdempotencyMiddleware_EmptyClientKey_DoesNotSuppressOtherCommands(t *testing.T) {
	store := newMockIdempotencyStore()
	mw := IdempotencyMiddleware(DefaultIdempotencyConfig(store))

	calls := 0
	wrapped := mw(countingHandler(&calls))

	_, err := wrapped(context.Background(), emptyKeyIdempotentCommand{Value: "first"})
	require.NoError(t, err)
	_, err = wrapped(context.Background(), emptyKeyIdempotentCommand{Value: "second"})
	require.NoError(t, err)

	assert.Equal(t, 2, calls, "a second, different command must not be replayed as the first")
	assert.Len(t, store.records, 2)
}

// --- Serialization failure --------------------------------------------------

func TestGenerateIdempotencyKey_MarshalFailure_ReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", GenerateIdempotencyKey(unmarshalableCommand{}))
	assert.Equal(t, "", GetIdempotencyKey(unmarshalableCommand{}))
}

func TestIdempotencyMiddleware_EmptyKey_PassesThrough(t *testing.T) {
	store := newMockIdempotencyStore()
	mw := IdempotencyMiddleware(DefaultIdempotencyConfig(store))

	calls := 0
	wrapped := mw(countingHandler(&calls))

	for i := 0; i < 2; i++ {
		result, err := wrapped(context.Background(), unmarshalableCommand{})
		require.NoError(t, err)
		assert.True(t, result.IsSuccess())
	}

	assert.Equal(t, 2, calls, "a command without a derivable key has no idempotency guarantee and must run every time")
	assert.Empty(t, store.records, "nothing may be stored under an empty key")
}

func TestIdempotencyMiddleware_EmptyKeyFromCustomGenerator_PassesThrough(t *testing.T) {
	store := newMockIdempotencyStore()
	cfg := DefaultIdempotencyConfig(store)
	cfg.KeyGenerator = func(Command) string { return "" }
	mw := IdempotencyMiddleware(cfg)

	calls := 0
	wrapped := mw(countingHandler(&calls))
	_, _ = wrapped(context.Background(), idempotencyTestCommand{Value: "x"})
	_, _ = wrapped(context.Background(), idempotencyTestCommand{Value: "x"})

	assert.Equal(t, 2, calls)
	assert.Empty(t, store.records)
}

func TestIdempotencyKeyPrefix_EmptyKey_StaysEmpty(t *testing.T) {
	gen := IdempotencyKeyPrefix("svc")
	assert.Equal(t, "", gen(unmarshalableCommand{}), "a bare prefix must never become a shared key")
	assert.Equal(t, "svc:custom", gen(idempotentTestCommand{IdempotencyID: "custom"}))
}

func TestIdempotencyKeyFromField_EmptyFieldAndUnmarshalable_ReturnsEmpty(t *testing.T) {
	gen := IdempotencyKeyFromField(func(Command) string { return "" })
	assert.Equal(t, "", gen(unmarshalableCommand{}))
}

// --- Scope ------------------------------------------------------------------

func TestDefaultIdempotencyScope(t *testing.T) {
	cmd := idempotencyTestCommand{Value: "x"}
	assert.Equal(t, "", DefaultIdempotencyScope(context.Background(), cmd))
	assert.Equal(t, "tenant-a", DefaultIdempotencyScope(WithTenantID(context.Background(), "tenant-a"), cmd))
}

func TestDefaultIdempotencyConfig_UsesTenantScope(t *testing.T) {
	cfg := DefaultIdempotencyConfig(newMockIdempotencyStore())
	require.NotNil(t, cfg.Scope)
	assert.Equal(t, "t1", cfg.Scope(WithTenantID(context.Background(), "t1"), idempotencyTestCommand{}))
}

func TestIdempotencyMiddleware_ScopesKeyByTenant(t *testing.T) {
	store := newMockIdempotencyStore()
	mw := IdempotencyMiddleware(DefaultIdempotencyConfig(store))

	calls := 0
	wrapped := mw(countingHandler(&calls))
	cmd := idempotentTestCommand{Value: "v", IdempotencyID: "req-1"}
	ctxA := WithTenantID(context.Background(), "tenant-a")
	ctxB := WithTenantID(context.Background(), "tenant-b")

	resA, err := wrapped(ctxA, cmd)
	require.NoError(t, err)
	resB, err := wrapped(ctxB, cmd)
	require.NoError(t, err)

	assert.Equal(t, 2, calls, "the same client key under another tenant must not replay the first tenant's result")
	assert.Equal(t, "agg-tenant-a", resA.AggregateID)
	assert.Equal(t, "agg-tenant-b", resB.AggregateID)

	_, ok := store.records["8:tenant-a|req-1"]
	assert.True(t, ok, "key must be scoped by tenant-a")
	_, ok = store.records["8:tenant-b|req-1"]
	assert.True(t, ok, "key must be scoped by tenant-b")
	_, ok = store.records["req-1"]
	assert.False(t, ok, "the unscoped key must not be used when a tenant is present")

	// Replay within the same tenant still works.
	resA2, err := wrapped(ctxA, cmd)
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "same tenant + same key must replay")
	assert.Equal(t, "agg-tenant-a", resA2.AggregateID)
}

func TestIdempotencyMiddleware_NoTenant_KeyIsUnscoped(t *testing.T) {
	store := newMockIdempotencyStore()
	mw := IdempotencyMiddleware(DefaultIdempotencyConfig(store))

	calls := 0
	_, err := mw(countingHandler(&calls))(context.Background(), idempotentTestCommand{IdempotencyID: "req-1"})
	require.NoError(t, err)

	_, ok := store.records["req-1"]
	assert.True(t, ok, "without a tenant the client key is used verbatim")
}

func TestIdempotencyMiddleware_CustomScope(t *testing.T) {
	store := newMockIdempotencyStore()
	cfg := DefaultIdempotencyConfig(store)
	cfg.Scope = func(ctx context.Context, _ Command) string { return ActorFromContext(ctx) }
	mw := IdempotencyMiddleware(cfg)

	calls := 0
	ctx := WithActor(WithTenantID(context.Background(), "tenant-a"), "alice")
	_, err := mw(countingHandler(&calls))(ctx, idempotentTestCommand{IdempotencyID: "req-1"})
	require.NoError(t, err)

	_, ok := store.records["5:alice|req-1"]
	assert.True(t, ok, "a custom scope replaces the tenant scope")
}

func TestIdempotencyMiddleware_ScopeDisabled(t *testing.T) {
	store := newMockIdempotencyStore()
	cfg := DefaultIdempotencyConfig(store)
	cfg.Scope = func(context.Context, Command) string { return "" }
	mw := IdempotencyMiddleware(cfg)

	calls := 0
	ctx := WithTenantID(context.Background(), "tenant-a")
	_, err := mw(countingHandler(&calls))(ctx, idempotentTestCommand{IdempotencyID: "req-1"})
	require.NoError(t, err)

	_, ok := store.records["req-1"]
	assert.True(t, ok, "a scope that returns \"\" leaves the key unscoped")
}

func TestIdempotencyMiddleware_NilScope_DefaultsToTenant(t *testing.T) {
	store := newMockIdempotencyStore()
	mw := IdempotencyMiddleware(IdempotencyConfig{Store: store}) // Scope nil

	calls := 0
	ctx := WithTenantID(context.Background(), "tenant-a")
	_, err := mw(countingHandler(&calls))(ctx, idempotentTestCommand{IdempotencyID: "req-1"})
	require.NoError(t, err)

	_, ok := store.records["8:tenant-a|req-1"]
	assert.True(t, ok)
}

// --- Length bound -----------------------------------------------------------

func TestEffectiveIdempotencyKey(t *testing.T) {
	longKey := strings.Repeat("k", 1000)
	tests := []struct {
		name    string
		cmdType string
		scope   string
		key     string
		want    string // exact expectation when hashed is false
		hashed  bool
	}{
		{name: "empty key stays empty", cmdType: "T", key: "", want: ""},
		{name: "empty key with scope stays empty", cmdType: "T", scope: "tenant", key: "", want: ""},
		{name: "no scope passes key through", cmdType: "T", key: "abc", want: "abc"},
		{name: "scope is prefixed", cmdType: "T", scope: "tenant-a", key: "abc", want: "8:tenant-a|abc"},
		{
			name: "exactly max length is kept", cmdType: "T",
			key:  strings.Repeat("k", MaxIdempotencyKeyLength),
			want: strings.Repeat("k", MaxIdempotencyKeyLength),
		},
		{name: "1000-char key is hashed", cmdType: "CreateOrder", key: longKey, hashed: true},
		{name: "one byte over the limit is hashed", cmdType: "CreateOrder", key: strings.Repeat("k", MaxIdempotencyKeyLength+1), hashed: true},
		{
			name: "scope pushing over the limit is hashed", cmdType: "CreateOrder",
			scope: strings.Repeat("s", 200), key: strings.Repeat("k", 100), hashed: true,
		},
		{name: "short scope with long key keeps the scope prefix visible", cmdType: "CreateOrder", scope: "tenant-a", key: longKey, hashed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveIdempotencyKey(tt.cmdType, tt.scope, tt.key)
			if !tt.hashed {
				assert.Equal(t, tt.want, got)
				return
			}
			assert.LessOrEqual(t, len(got), MaxIdempotencyKeyLength)
			// The visible prefix is "<len>:<scope>|" + cmdType (truncated to fit); the
			// 64-hex digest is always intact.
			idx := strings.LastIndex(got, ":sha256:")
			require.GreaterOrEqual(t, idx, 0, "got %q", got)
			assert.Len(t, got[idx+len(":sha256:"):], 64, "sha256 hex digest")
			wantPrefix := tt.cmdType
			if tt.scope != "" {
				wantPrefix = strconv.Itoa(len(tt.scope)) + ":" + tt.scope + "|" + tt.cmdType
			}
			assert.True(t, strings.HasPrefix(wantPrefix, got[:idx]), "visible prefix %q must be a prefix of %q", got[:idx], wantPrefix)
			assert.Equal(t, got, EffectiveIdempotencyKey(tt.cmdType, tt.scope, tt.key), "hashing must be deterministic")
		})
	}
}

func TestEffectiveIdempotencyKey_HashCoversScope(t *testing.T) {
	longKey := strings.Repeat("k", 1000)
	a := EffectiveIdempotencyKey("T", "tenant-a", longKey)
	b := EffectiveIdempotencyKey("T", "tenant-b", longKey)
	assert.NotEqual(t, a, b, "two tenants presenting the same over-long key must still get distinct keys")
	assert.True(t, strings.HasPrefix(a, "8:tenant-a|T:sha256:"), "the hashed form keeps the scope in the clear: %q", a)
	assert.True(t, strings.HasPrefix(b, "8:tenant-b|T:sha256:"), "the hashed form keeps the scope in the clear: %q", b)

	// A scope too long to show in full is still covered by the digest.
	longScope := strings.Repeat("s", 300)
	c := EffectiveIdempotencyKey("T", longScope, longKey)
	d := EffectiveIdempotencyKey("T", longScope+"x", longKey)
	assert.Len(t, c, MaxIdempotencyKeyLength)
	assert.NotEqual(t, c, d)
}

func TestEffectiveIdempotencyKey_ScopeEncodingIsInjective(t *testing.T) {
	// The classic "scope|key" ambiguity: tenant "a" with client key "b|req-1" vs
	// tenant "a|b" with client key "req-1". The length prefix keeps them apart.
	tests := []struct {
		name         string
		scopeA, keyA string
		scopeB, keyB string
		wantA, wantB string
	}{
		{"pipe in client key", "a", "b|req-1", "a|b", "req-1", "1:a|b|req-1", "3:a|b|req-1"},
		{"colon and digits in client key", "1", "2:x|k", "1|2:x", "k", "1:1|2:x|k", "5:1|2:x|k"},
		{"client key mimicking an encoded scope", "t", "1:t|k", "t|1:t", "k", "1:t|1:t|k", "5:t|1:t|k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotA := EffectiveIdempotencyKey("T", tt.scopeA, tt.keyA)
			gotB := EffectiveIdempotencyKey("T", tt.scopeB, tt.keyB)
			assert.Equal(t, tt.wantA, gotA)
			assert.Equal(t, tt.wantB, gotB)
			assert.NotEqual(t, gotA, gotB, "distinct (scope, key) pairs must never share a stored key")
		})
	}
}

func TestIdempotencyMiddleware_PipeInClientKey_DoesNotCrossScopes(t *testing.T) {
	store := newMockIdempotencyStore()
	mw := IdempotencyMiddleware(DefaultIdempotencyConfig(store))

	calls := 0
	wrapped := mw(countingHandler(&calls))

	// Tenant "a|b" processes "req-1" first…
	_, err := wrapped(WithTenantID(context.Background(), "a|b"), idempotentTestCommand{Value: "v", IdempotencyID: "req-1"})
	require.NoError(t, err)
	// …then tenant "a" presents the crafted key "b|req-1": it must NOT replay the
	// other tenant's result.
	res, err := wrapped(WithTenantID(context.Background(), "a"), idempotentTestCommand{Value: "v", IdempotencyID: "b|req-1"})
	require.NoError(t, err)

	assert.Equal(t, 2, calls, "a crafted client key must not alias another tenant's key")
	assert.Equal(t, "agg-a", res.AggregateID)
	assert.Len(t, store.records, 2)
}

// --- Record field bounds ----------------------------------------------------

func TestNewIdempotencyRecord_TruncatesCommandTypeAndAggregateID(t *testing.T) {
	tests := []struct {
		name        string
		cmdType     string
		aggregateID string
	}{
		{"ascii over the limit", strings.Repeat("T", 1000), strings.Repeat("a", 1000)},
		{"multibyte over the limit", strings.Repeat("é", 300), strings.Repeat("日", 300)},
		{"exactly at the limit", strings.Repeat("T", MaxIdempotencyFieldLength), strings.Repeat("a", MaxIdempotencyFieldLength)},
		{"short values untouched", "CreateOrder", "order-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := NewIdempotencyRecord("key", tt.cmdType, NewSuccessResult(tt.aggregateID, 1), time.Hour)

			assert.LessOrEqual(t, len(rec.CommandType), MaxIdempotencyFieldLength)
			assert.LessOrEqual(t, len(rec.AggregateID), MaxIdempotencyFieldLength)
			assert.True(t, utf8.ValidString(rec.CommandType), "truncation must not split a rune")
			assert.True(t, utf8.ValidString(rec.AggregateID), "truncation must not split a rune")
			assert.True(t, strings.HasPrefix(tt.cmdType, rec.CommandType))
			assert.True(t, strings.HasPrefix(tt.aggregateID, rec.AggregateID))
			if len(tt.cmdType) <= MaxIdempotencyFieldLength {
				assert.Equal(t, tt.cmdType, rec.CommandType, "values that fit are stored verbatim")
				assert.Equal(t, tt.aggregateID, rec.AggregateID)
			}
			assert.Equal(t, "key", rec.Key, "the key is never touched here")
		})
	}
}

func TestNewProcessingRecord_TruncatesCommandType(t *testing.T) {
	rec := newProcessingRecord("key", strings.Repeat("é", 300), time.Minute)
	assert.LessOrEqual(t, len(rec.CommandType), MaxIdempotencyFieldLength)
	assert.True(t, utf8.ValidString(rec.CommandType))
	assert.True(t, isProcessingRecord(rec))
}

// columnWidthIdempotencyStore mimics the PostgreSQL store's VARCHAR(255)
// columns: any record whose key, command type or aggregate id exceeds 255 bytes
// fails the insert, the way an over-long value does in production.
type columnWidthIdempotencyStore struct {
	*mockIdempotencyStore
}

func (s *columnWidthIdempotencyStore) check(r *IdempotencyRecord) error {
	if len(r.Key) > 255 || len(r.CommandType) > 255 || len(r.AggregateID) > 255 {
		return errors.New("pq: value too long for type character varying(255)")
	}
	return nil
}

func (s *columnWidthIdempotencyStore) Store(ctx context.Context, r *IdempotencyRecord) error {
	if err := s.check(r); err != nil {
		return err
	}
	return s.mockIdempotencyStore.Store(ctx, r)
}

func (s *columnWidthIdempotencyStore) StoreIfAbsent(ctx context.Context, r *IdempotencyRecord) (bool, error) {
	if err := s.check(r); err != nil {
		return false, err
	}
	return s.mockIdempotencyStore.StoreIfAbsent(ctx, r)
}

// longTypeCommand has a command type far wider than the store's column.
type longTypeCommand struct {
	CommandBase
	Value string
}

func (c longTypeCommand) CommandType() string    { return strings.Repeat("LongType", 100) }
func (c longTypeCommand) Validate() error        { return nil }
func (c longTypeCommand) IdempotencyKey() string { return "req-1" }

func TestIdempotencyMiddleware_OverLongCommandTypeAndAggregateID_StillDedupes(t *testing.T) {
	store := &columnWidthIdempotencyStore{mockIdempotencyStore: newMockIdempotencyStore()}
	mw := IdempotencyMiddleware(DefaultIdempotencyConfig(store)) // fail-open by default

	calls := 0
	wrapped := mw(func(ctx context.Context, cmd Command) (CommandResult, error) {
		calls++
		return NewSuccessResult(strings.Repeat("a", 1000), int64(calls)), nil
	})

	first, err := wrapped(context.Background(), longTypeCommand{Value: "v"})
	require.NoError(t, err)
	assert.True(t, first.IsSuccess())
	require.Len(t, store.records, 1, "the result record must have been accepted by the width-checking store")

	second, err := wrapped(context.Background(), longTypeCommand{Value: "v"})
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "an over-long command type / aggregate id must not silently defeat deduplication under fail-open")
	assert.True(t, second.IsSuccess())
	assert.LessOrEqual(t, len(second.AggregateID), MaxIdempotencyFieldLength, "the replayed aggregate id is the stored (bounded) one")
}

func TestEffectiveIdempotencyKey_LongCommandType_StillFits(t *testing.T) {
	got := EffectiveIdempotencyKey(strings.Repeat("T", 300), "", strings.Repeat("k", 1000))

	assert.Len(t, got, MaxIdempotencyKeyLength)
	idx := strings.LastIndex(got, ":sha256:")
	require.GreaterOrEqual(t, idx, 0)
	assert.Len(t, got[idx+len(":sha256:"):], 64, "the digest must be kept intact; only the type prefix is truncated")
}

func TestEffectiveIdempotencyKey_MultibyteCommandType_TruncatesOnRuneBoundary(t *testing.T) {
	got := EffectiveIdempotencyKey(strings.Repeat("é", 200), "", strings.Repeat("k", 1000))

	assert.LessOrEqual(t, len(got), MaxIdempotencyKeyLength)
	assert.True(t, utf8.ValidString(got), "truncation must not split a rune")
	assert.Contains(t, got, ":sha256:")
}

func TestIdempotencyMiddleware_LongClientKey_IsHashedAndReplays(t *testing.T) {
	store := newMockIdempotencyStore()
	mw := IdempotencyMiddleware(DefaultIdempotencyConfig(store))

	calls := 0
	wrapped := mw(countingHandler(&calls))
	cmd := idempotentTestCommand{Value: "v", IdempotencyID: strings.Repeat("k", 1000)}

	first, err := wrapped(context.Background(), cmd)
	require.NoError(t, err)
	assert.True(t, first.IsSuccess())
	require.Len(t, store.records, 1)
	for key := range store.records {
		assert.LessOrEqual(t, len(key), MaxIdempotencyKeyLength, "stored key must fit the store column")
		assert.True(t, strings.HasPrefix(key, "IdempotentTestCommand:sha256:"), "got %q", key)
	}

	second, err := wrapped(context.Background(), cmd)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "the hashed key must still deduplicate")
	assert.Equal(t, first.AggregateID, second.AggregateID)
}

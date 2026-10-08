package mink

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"
	"unicode/utf8"

	"go-mink.dev/adapters"
)

// Re-export types from the adapters package for convenience.
type (
	// AuditStore persists and queries the command audit trail.
	AuditStore = adapters.AuditStore

	// AuditEntry is a single immutable record in the command audit trail.
	//
	// The audit trail is stored in plaintext: it is not covered by field-level
	// encryption or crypto-shredding, so any personal data that reaches it (via
	// Metadata, Actor or Error strings) can only be erased through the audit
	// subject eraser (NewAuditSubjectEraser). AuditMiddleware caps the bounded
	// string fields (CommandType, CommandID, AggregateID, Actor, TenantID,
	// CorrelationID, CausationID) at MaxAuditFieldLength bytes before Append.
	AuditEntry = adapters.AuditEntry

	// AuditQuery filters and paginates a query against the audit trail.
	AuditQuery = adapters.AuditQuery

	// AuditOrder controls how audit entries are sorted when queried.
	AuditOrder = adapters.AuditOrder

	// SubjectAuditPurger is the optional AuditStore extension for GDPR erasure of a
	// subject's audit trail (see NewAuditSubjectEraser).
	SubjectAuditPurger = adapters.SubjectAuditPurger
)

// Re-export audit ordering constants for convenience.
const (
	// AuditOrderTimestampDesc returns the most recent entries first (default).
	AuditOrderTimestampDesc = adapters.AuditOrderTimestampDesc

	// AuditOrderTimestampAsc returns the oldest entries first.
	AuditOrderTimestampAsc = adapters.AuditOrderTimestampAsc
)

// MaxAuditFieldLength is the maximum length, in bytes, that AuditMiddleware
// allows for the AuditEntry string fields stored in bounded columns:
// CommandType, CommandID, AggregateID, Actor, TenantID, CorrelationID and
// CausationID. It matches the PostgreSQL audit table's VARCHAR(255) columns.
// Longer values are truncated on a rune boundary before Append, so an
// attacker-supplied over-long actor or command ID cannot make the audit row
// fail (and, under the default fail-open policy, silently drop the record).
// Error and Metadata are unbounded (TEXT / JSONB).
const MaxAuditFieldLength = 255

// ErrNilAuditEntry is returned by AuditStore.Append when the entry is nil.
var ErrNilAuditEntry = adapters.ErrNilAuditEntry

// =============================================================================
// Actor context helpers
// =============================================================================

// actorKey is the context key for the audit actor.
type actorKey struct{}

// WithActor returns a context with the audit actor set.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFromContext returns the audit actor from context, or "" if not set.
func ActorFromContext(ctx context.Context) string {
	if actor, ok := ctx.Value(actorKey{}).(string); ok {
		return actor
	}
	return ""
}

// ActorFunc resolves the actor responsible for a command.
type ActorFunc func(ctx context.Context, cmd Command) string

// defaultActorFunc resolves the actor from the context (set via WithActor).
func defaultActorFunc(ctx context.Context, _ Command) string {
	return ActorFromContext(ctx)
}

// =============================================================================
// Audit middleware
// =============================================================================

// AuditConfig configures the audit logging middleware.
type AuditConfig struct {
	// Store is the audit store that persists the trail. Required.
	Store AuditStore

	// ActorFunc resolves the actor for each command. If nil, the actor is read
	// from the context via ActorFromContext.
	ActorFunc ActorFunc

	// SkipCommands lists command types that should not be audited.
	SkipCommands []string

	// FailClosed determines behavior when the audit store write fails. If true,
	// the audit write failure is surfaced as the command result/error (note: the
	// command's side effect has already run — auditing is not transactional). If
	// false (the default), the original command result is returned (fail-open)
	// and the dropped entry is reported through Logger and OnError so it is
	// never silent.
	FailClosed bool

	// IncludeMetadata copies the command's metadata map into the audit entry when
	// the command exposes one via GetMetadataMap() map[string]string. The audit
	// trail is plaintext (see AuditEntry), so use MetadataFilter to keep
	// personal or secret values out of it.
	IncludeMetadata bool

	// MetadataFilter, when set, is applied to the copy of the command's metadata
	// that IncludeMetadata captured, before the entry is written. Use it to drop
	// or mask keys that carry personal or secret data: the audit trail is
	// stored in plaintext and PII that reaches it can only be erased through
	// the audit subject eraser. The filter receives a private copy it may mutate
	// or replace; returning nil or an empty map omits metadata. It is not
	// called when the command has no metadata.
	MetadataFilter func(map[string]string) map[string]string

	// Logger receives a warning whenever an audit entry is dropped under the
	// default fail-open policy, and when the middleware is constructed without
	// a store, so an accountability gap is never silent. If nil, warnings go to
	// log/slog's process-wide default logger (slog.Default()), which the
	// application controls via slog.SetDefault.
	Logger Logger

	// OnError is an optional hook invoked whenever Store.Append fails, in both
	// fail-open and fail-closed mode, with the entry that could not be
	// persisted. Use it to count drops, raise an alert, or spool the entry
	// elsewhere. It runs inline on the command path, so it must not block.
	OnError func(ctx context.Context, entry *AuditEntry, err error)

	// now returns the current time. Injectable for deterministic tests.
	now func() time.Time

	// idgen generates audit entry IDs. Injectable for deterministic tests.
	idgen func() string
}

// DefaultAuditConfig returns a default audit configuration for the given store.
func DefaultAuditConfig(store AuditStore) AuditConfig {
	return AuditConfig{
		Store:     store,
		ActorFunc: defaultActorFunc,
	}
}

// slogDefaultLogger adapts log/slog's process-wide default logger to the Logger
// interface. It resolves slog.Default() on every call, so a logger installed
// with slog.SetDefault after the middleware was built is still honoured.
type slogDefaultLogger struct{}

func (slogDefaultLogger) Debug(msg string, args ...interface{}) { slog.Default().Debug(msg, args...) }
func (slogDefaultLogger) Info(msg string, args ...interface{})  { slog.Default().Info(msg, args...) }
func (slogDefaultLogger) Warn(msg string, args ...interface{})  { slog.Default().Warn(msg, args...) }
func (slogDefaultLogger) Error(msg string, args ...interface{}) { slog.Default().Error(msg, args...) }

// defaultAuditLogger returns the logger used when AuditConfig.Logger is nil.
// Unlike EventStore, whose default logger is a no-op, the audit middleware
// defaults to a visible logger: a silently dropped audit entry is an
// accountability gap, not just noise.
func defaultAuditLogger() Logger {
	return slogDefaultLogger{}
}

// newAuditID returns a random RFC 4122 version 4 UUID string.
// It uses crypto/rand to avoid introducing a UUID dependency.
func newAuditID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should never fail; derive best-effort bytes from the clock so
		// the result is still a syntactically valid v4 UUID (it may be stored in a
		// PostgreSQL UUID column).
		now := uint64(time.Now().UnixNano())
		binary.BigEndian.PutUint64(b[0:8], now)
		binary.BigEndian.PutUint64(b[8:16], now^0x9e3779b97f4a7c15)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b[0:4]) + "-" +
		hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" +
		hex.EncodeToString(b[10:16])
}

// copyMetadataMap returns a defensive copy of a command's metadata map, or nil.
func copyMetadataMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// truncateUTF8Bytes returns s cut to at most maxBytes bytes without splitting a
// multi-byte rune. A string that already fits is returned unchanged.
func truncateUTF8Bytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// boundAuditEntry truncates the entry's bounded string fields to
// MaxAuditFieldLength bytes so values a client controls (command ID, actor,
// correlation/causation IDs, ...) cannot overflow the store's columns and
// make the row fail.
func boundAuditEntry(e *AuditEntry) {
	e.CommandType = truncateUTF8Bytes(e.CommandType, MaxAuditFieldLength)
	e.CommandID = truncateUTF8Bytes(e.CommandID, MaxAuditFieldLength)
	e.AggregateID = truncateUTF8Bytes(e.AggregateID, MaxAuditFieldLength)
	e.Actor = truncateUTF8Bytes(e.Actor, MaxAuditFieldLength)
	e.TenantID = truncateUTF8Bytes(e.TenantID, MaxAuditFieldLength)
	e.CorrelationID = truncateUTF8Bytes(e.CorrelationID, MaxAuditFieldLength)
	e.CausationID = truncateUTF8Bytes(e.CausationID, MaxAuditFieldLength)
}

// foldAuditFailure folds an audit-side failure (a store write error, or a nil
// store under FailClosed) into the command outcome:
//
//   - If the command already failed (via err or an error CommandResult), auditErr
//     is surfaced alongside that failure — joined with the command's error when it
//     has one — so the command's own failure is preserved and never masked.
//   - If the command succeeded, there is no prior error to join, so its successful
//     result is replaced with an error result carrying auditErr: under fail-closed,
//     an audit failure is itself a command failure.
func foldAuditFailure(result CommandResult, err, auditErr error) (CommandResult, error) {
	switch {
	case err != nil:
		return result, errors.Join(err, auditErr)
	case result.IsError():
		if result.Error != nil {
			return result, errors.Join(result.Error, auditErr)
		}
		return result, auditErr
	default:
		return NewErrorResult(auditErr), auditErr
	}
}

// AuditMiddleware creates middleware that writes an immutable audit entry for
// every dispatched command. Both successful and failed executions are audited.
//
// The audit write happens after the command runs. By default the middleware is
// fail-open: if the store write fails, the original command result is returned
// and the dropped entry is reported through Logger (a warning) and the OnError
// hook, so the drop is visible. Set FailClosed to surface the audit write
// failure instead — note that the command's side effect has already happened,
// so this is not a transactional guarantee.
//
// The bounded string fields of each entry are capped at MaxAuditFieldLength
// bytes before the write (see boundAuditEntry), so client-controlled values
// cannot overflow the store's columns. The trail itself is plaintext: anything
// copied into it (IncludeMetadata, actor, error strings) is not encrypted and
// can only be erased via the audit subject eraser, so filter PII out with
// MetadataFilter rather than relying on crypto-shredding.
//
// This middleware does not recover panics itself. To audit a handler that
// panics, place RecoveryMiddleware *inside* this one (i.e. closer to the
// handler) so the panic is converted to an error result before the audit entry
// is written, e.g.:
//
//	mink.ChainMiddleware(mink.AuditMiddleware(cfg), mink.RecoveryMiddleware())
func AuditMiddleware(config AuditConfig) Middleware {
	if config.ActorFunc == nil {
		config.ActorFunc = defaultActorFunc
	}
	if config.Logger == nil {
		config.Logger = defaultAuditLogger()
	}
	if config.now == nil {
		config.now = time.Now
	}
	if config.idgen == nil {
		config.idgen = newAuditID
	}

	skipSet := make(map[string]bool, len(config.SkipCommands))
	for _, t := range config.SkipCommands {
		skipSet[t] = true
	}

	// A nil store under fail-open silently audits nothing. Say so once, here,
	// rather than per dispatch (CommandBus rebuilds the chain on every call).
	if config.Store == nil && !config.FailClosed {
		config.Logger.Warn("mink: audit middleware configured without a store; commands will not be audited")
	}

	return func(next MiddlewareFunc) MiddlewareFunc {
		// A nil store can never persist anything, and whether it is nil is fixed at
		// construction — so decide the policy once here rather than on every
		// dispatch. Fail-open degenerates to a pass-through (auditing never breaks
		// command processing); fail-closed surfaces the misconfiguration for every
		// command that would otherwise be audited.
		if config.Store == nil {
			if !config.FailClosed {
				return next
			}
			return func(ctx context.Context, cmd Command) (CommandResult, error) {
				result, err := next(ctx, cmd)
				if skipSet[cmd.CommandType()] {
					return result, err // excluded commands are never audited
				}
				return foldAuditFailure(result, err, ErrNilAuditStore)
			}
		}

		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			// Skip auditing for excluded command types.
			if skipSet[cmd.CommandType()] {
				return next(ctx, cmd)
			}

			start := config.now()
			result, err := next(ctx, cmd)
			end := config.now()

			entry := &AuditEntry{
				ID:            config.idgen(),
				Timestamp:     end,
				CommandType:   cmd.CommandType(),
				AggregateID:   result.AggregateID,
				Version:       result.Version,
				Actor:         config.ActorFunc(ctx, cmd),
				TenantID:      TenantIDFromContext(ctx),
				CorrelationID: CorrelationIDFromContext(ctx),
				CausationID:   CausationIDFromContext(ctx),
				Success:       err == nil && result.IsSuccess(),
			}
			// DurationMs reflects only handler execution time (start→end) and shares
			// the same end reading as Timestamp, so the two never drift.
			entry.DurationMs = end.Sub(start).Milliseconds()

			// Command ID, if the command exposes one.
			if c, ok := cmd.(interface{ GetCommandID() string }); ok {
				entry.CommandID = c.GetCommandID()
			}

			// Fall back to the command's aggregate ID when the result has none.
			if entry.AggregateID == "" {
				if ac, ok := cmd.(AggregateCommand); ok {
					entry.AggregateID = ac.AggregateID()
				}
			}

			// Record the failure message from the error or the result.
			if err != nil {
				entry.Error = err.Error()
			} else if result.Error != nil {
				entry.Error = result.Error.Error()
			}

			// Optionally capture the command's metadata map, filtered if configured.
			if config.IncludeMetadata {
				if mc, ok := cmd.(interface{ GetMetadataMap() map[string]string }); ok {
					entry.Metadata = copyMetadataMap(mc.GetMetadataMap())
					if entry.Metadata != nil && config.MetadataFilter != nil {
						entry.Metadata = config.MetadataFilter(entry.Metadata)
						if len(entry.Metadata) == 0 {
							entry.Metadata = nil
						}
					}
				}
			}

			// Cap client-controlled values so they cannot make the row fail.
			boundAuditEntry(entry)

			if appendErr := config.Store.Append(ctx, entry); appendErr != nil {
				if config.OnError != nil {
					config.OnError(ctx, entry, appendErr)
				}
				if config.FailClosed {
					return foldAuditFailure(result, err, appendErr)
				}
				// Fail-open: the command outcome stands, but the drop must be visible.
				config.Logger.Warn("mink: audit entry dropped: store write failed (fail-open)",
					"auditId", entry.ID,
					"commandType", entry.CommandType,
					"commandId", entry.CommandID,
					"aggregateId", entry.AggregateID,
					"tenantId", entry.TenantID,
					"error", appendErr,
				)
			}

			return result, err
		}
	}
}

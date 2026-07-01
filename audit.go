package mink

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"time"

	"go-mink.dev/adapters"
)

// Re-export types from the adapters package for convenience.
type (
	// AuditStore persists and queries the command audit trail.
	AuditStore = adapters.AuditStore

	// AuditEntry is a single immutable record in the command audit trail.
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
	// false (the default), the failure is ignored (fail-open) and the original
	// command result is returned.
	FailClosed bool

	// IncludeMetadata copies the command's metadata map into the audit entry when
	// the command exposes one via GetMetadataMap() map[string]string.
	IncludeMetadata bool

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
// fail-open: if the store write fails, the failure is ignored and the original
// command result is returned. Set FailClosed to surface the audit write failure
// instead — note that the command's side effect has already happened, so this is
// not a transactional guarantee.
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

			// Optionally capture the command's metadata map.
			if config.IncludeMetadata {
				if mc, ok := cmd.(interface{ GetMetadataMap() map[string]string }); ok {
					entry.Metadata = copyMetadataMap(mc.GetMetadataMap())
				}
			}

			if appendErr := config.Store.Append(ctx, entry); appendErr != nil && config.FailClosed {
				return foldAuditFailure(result, err, appendErr)
			}

			return result, err
		}
	}
}

package mink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"go-mink.dev/adapters"
)

// Re-export types from adapters package for convenience
type (
	// IdempotencyStore tracks processed commands to prevent duplicate processing.
	IdempotencyStore = adapters.IdempotencyStore

	// IdempotencyRecord stores information about a processed command.
	IdempotencyRecord = adapters.IdempotencyRecord

	// SubjectIdempotencyPurger is the optional IdempotencyStore extension for GDPR
	// erasure of a subject's idempotency records (see NewIdempotencySubjectEraser).
	SubjectIdempotencyPurger = adapters.SubjectIdempotencyPurger
)

// MaxIdempotencyKeyLength is the maximum length, in bytes, of the key that
// IdempotencyMiddleware hands to the IdempotencyStore. It matches the
// PostgreSQL store's `key VARCHAR(255)` column: a longer key would fail the
// insert and, under the default fail-open policy, silently defeat
// deduplication. Keys that would exceed it are replaced by a fixed-length
// digest; see EffectiveIdempotencyKey.
const MaxIdempotencyKeyLength = 255

// MaxIdempotencyFieldLength is the maximum length, in bytes, of the CommandType
// and AggregateID stored on an IdempotencyRecord. It matches the PostgreSQL
// store's `command_type VARCHAR(255)` and `aggregate_id VARCHAR(255)` columns:
// a longer value would fail the insert and, under the default fail-open policy,
// silently defeat deduplication exactly like an over-long key. Longer values are
// truncated on a rune boundary by NewIdempotencyRecord (and by the in-flight
// reservation record the middleware writes before the handler runs).
const MaxIdempotencyFieldLength = 255

// IdempotencyReplayError indicates a command was already processed.
type IdempotencyReplayError struct {
	Key     string
	Message string
}

func (e *IdempotencyReplayError) Error() string {
	if e.Message != "" {
		return "mink: command already processed with key " + e.Key + ": " + e.Message
	}
	return "mink: command already processed with key " + e.Key
}

func (e *IdempotencyReplayError) Is(target error) bool {
	return target == ErrCommandAlreadyProcessed
}

func (e *IdempotencyReplayError) Unwrap() error {
	return ErrCommandAlreadyProcessed
}

// NewIdempotencyRecord creates a new IdempotencyRecord from a CommandResult.
//
// The record's CommandType and AggregateID are truncated on a rune boundary to
// MaxIdempotencyFieldLength bytes so they always fit the store's columns; the
// key is stored as given (IdempotencyMiddleware bounds it beforehand, see
// EffectiveIdempotencyKey). Truncation only affects what is recorded for
// inspection — deduplication keys on Key alone.
func NewIdempotencyRecord(key, cmdType string, result CommandResult, ttl time.Duration) *IdempotencyRecord {
	now := time.Now()
	record := &IdempotencyRecord{
		Key:         key,
		CommandType: truncateUTF8Bytes(cmdType, MaxIdempotencyFieldLength),
		AggregateID: truncateUTF8Bytes(result.AggregateID, MaxIdempotencyFieldLength),
		Version:     result.Version,
		Success:     result.IsSuccess(),
		ProcessedAt: now,
		ExpiresAt:   now.Add(ttl),
	}

	if result.Error != nil {
		record.Error = result.Error.Error()
	}

	return record
}

// IdempotencyRecordToResult converts the record to a CommandResult.
func IdempotencyRecordToResult(r *IdempotencyRecord) CommandResult {
	if r.Success {
		return NewSuccessResult(r.AggregateID, r.Version)
	}
	if r.Error != "" {
		return NewErrorResult(&IdempotencyReplayError{
			Key:     r.Key,
			Message: r.Error,
		})
	}
	return NewErrorResult(&IdempotencyReplayError{
		Key:     r.Key,
		Message: "unknown error",
	})
}

// GenerateIdempotencyKey generates an idempotency key from a command.
// The key is based on the command type and its JSON-serialized content.
//
// If the command cannot be JSON-serialized it returns "", which means "no
// idempotency guarantee for this command": IdempotencyMiddleware passes such a
// command straight through to the handler instead of deduplicating it. It
// deliberately does not fall back to a type-only key, because that would make
// every later command of the same type collide on a single key for the TTL
// and suppress legitimate commands.
func GenerateIdempotencyKey(cmd Command) string {
	data, err := json.Marshal(cmd)
	if err != nil {
		return ""
	}

	hash := sha256.Sum256(data)
	return cmd.CommandType() + ":" + hex.EncodeToString(hash[:16])
}

// GetIdempotencyKey returns the idempotency key for a command.
//
// If the command implements IdempotentCommand and IdempotencyKey returns a
// non-empty key, that key is used. Otherwise, including when IdempotencyKey
// returns "", the key is generated from the command content via
// GenerateIdempotencyKey, so an IdempotentCommand that forgets to set its key
// never shares one global key with every other such command.
func GetIdempotencyKey(cmd Command) string {
	if ic, ok := cmd.(IdempotentCommand); ok {
		if key := ic.IdempotencyKey(); key != "" {
			return key
		}
	}
	return GenerateIdempotencyKey(cmd)
}

// DefaultIdempotencyScope is the default IdempotencyConfig.Scope. It scopes
// idempotency keys by the tenant ID carried in the context (see WithTenantID
// and TenantMiddleware) and returns "" when no tenant is set, which leaves the
// key unscoped.
func DefaultIdempotencyScope(ctx context.Context, _ Command) string {
	return TenantIDFromContext(ctx)
}

// EffectiveIdempotencyKey computes the key IdempotencyMiddleware hands to the
// store for a command of type cmdType whose generated (or client-supplied) key
// is key and whose principal scope is scope:
//
//   - An empty key stays empty: there is no idempotency guarantee and the
//     middleware passes the command through.
//   - A non-empty scope is prepended in a length-prefixed encoding,
//     strconv.Itoa(len(scope)) + ":" + scope + "|" + key — for example
//     "8:tenant-a|req-1" — so two principals (for example two tenants) that
//     present the same client key can never replay or suppress each other's
//     commands. Because the scope's byte length is spelled out, the encoding is
//     injective: no client-chosen key, even one containing '|' or ':', can make
//     one scope's key collide with another's (tenant "a" with key "b|x" and
//     tenant "a|b" with key "x" yield "1:a|b|x" and "3:a|b|x"). An empty scope
//     leaves the key unscoped and stored verbatim.
//   - If the result is longer than MaxIdempotencyKeyLength bytes it is replaced
//     by the same "len:scope|" prefix (empty when unscoped) + cmdType +
//     ":sha256:" + hex(SHA-256(result)), with the prefix+cmdType part truncated
//     on a rune boundary as needed so the whole key fits in
//     MaxIdempotencyKeyLength bytes. The digest always covers the full scoped
//     key, so scoping survives hashing even when the visible scope prefix had to
//     be truncated.
//
// It is exported so applications can predict, inspect or pre-seed the exact key
// the middleware will use.
func EffectiveIdempotencyKey(cmdType, scope, key string) string {
	if key == "" {
		return ""
	}
	scopePrefix := ""
	if scope != "" {
		scopePrefix = strconv.Itoa(len(scope)) + ":" + scope + "|"
	}
	scoped := scopePrefix + key
	if len(scoped) <= MaxIdempotencyKeyLength {
		return scoped
	}

	sum := sha256.Sum256([]byte(scoped))
	suffix := ":sha256:" + hex.EncodeToString(sum[:])
	return truncateUTF8Bytes(scopePrefix+cmdType, MaxIdempotencyKeyLength-len(suffix)) + suffix
}

// IdempotencyConfig configures the idempotency middleware.
type IdempotencyConfig struct {
	// Store is the idempotency store to use.
	Store IdempotencyStore

	// TTL is how long to keep idempotency records.
	// Default is 24 hours.
	TTL time.Duration

	// KeyGenerator generates idempotency keys from commands.
	// If nil, GetIdempotencyKey is used. A generator that returns "" declares
	// that the command carries no idempotency guarantee: the middleware passes
	// it through to the handler without deduplication.
	KeyGenerator func(Command) string

	// Scope returns the principal scope of a command's idempotency key, such as
	// the tenant the command runs for. When it returns a non-empty string the
	// key handed to the store becomes "<len(scope)>:" + scope + "|" + key (see
	// EffectiveIdempotencyKey), so principals can never replay or suppress each
	// other's commands by presenting the same client-supplied key.
	//
	// If nil, DefaultIdempotencyScope is used, which reads the tenant ID from
	// the context, so TenantMiddleware (or WithTenantID) must run before this
	// middleware for scoping to take effect. To disable scoping entirely,
	// supply a func that always returns "".
	Scope func(ctx context.Context, cmd Command) string

	// StoreErrors determines if failed commands should be stored.
	// If true, replaying a failed command returns the same error.
	// If false, failed commands can be retried.
	// Default is false.
	StoreErrors bool

	// FailClosed determines behavior when the idempotency store is unavailable.
	// If true, a store error fails the command (so a store outage cannot allow
	// duplicate processing). If false (the default), the command proceeds without
	// the idempotency guarantee (fail-open).
	FailClosed bool

	// ReservationTTL bounds how long an in-flight reservation blocks a key before
	// it self-expires. It should be longer than the slowest handler but much
	// shorter than TTL, so a process that crashes mid-handler does not block a
	// legitimate retry of the command for the full result TTL. Default: 5 minutes.
	ReservationTTL time.Duration

	// SkipCommands is a list of command types to skip idempotency checking.
	SkipCommands []string
}

// idempotencyProcessingMarker tags a reservation record that has not yet
// completed, distinguishing an in-flight reservation from a stored result.
const idempotencyProcessingMarker = "mink:processing"

// newProcessingRecord builds a reservation record used to claim a key before the
// handler runs. CommandType is bounded like NewIdempotencyRecord's so the
// reservation insert can never fail on the store's column width.
func newProcessingRecord(key, cmdType string, ttl time.Duration) *IdempotencyRecord {
	now := time.Now()
	return &IdempotencyRecord{
		Key:         key,
		CommandType: truncateUTF8Bytes(cmdType, MaxIdempotencyFieldLength),
		Success:     false,
		Error:       idempotencyProcessingMarker,
		ProcessedAt: now,
		ExpiresAt:   now.Add(ttl),
	}
}

// isProcessingRecord reports whether a record is an in-flight reservation.
func isProcessingRecord(r *IdempotencyRecord) bool {
	return r != nil && !r.Success && r.Error == idempotencyProcessingMarker
}

// DefaultIdempotencyConfig returns a default idempotency configuration.
func DefaultIdempotencyConfig(store IdempotencyStore) IdempotencyConfig {
	return IdempotencyConfig{
		Store:          store,
		TTL:            24 * time.Hour,
		KeyGenerator:   GetIdempotencyKey,
		Scope:          DefaultIdempotencyScope,
		StoreErrors:    false,
		ReservationTTL: 5 * time.Minute,
		SkipCommands:   nil,
	}
}

// IdempotencyMiddleware creates middleware that prevents duplicate command processing.
//
// The key looked up in the store is EffectiveIdempotencyKey(cmd.CommandType(),
// Scope(ctx, cmd), KeyGenerator(cmd)): scoped by principal (the tenant ID in
// the context by default) and bounded to MaxIdempotencyKeyLength bytes. A
// command for which no key can be derived (the generator returned "") is passed
// through to the handler with no idempotency guarantee rather than being
// collapsed onto a key shared with unrelated commands.
func IdempotencyMiddleware(config IdempotencyConfig) Middleware {
	if config.TTL <= 0 {
		config.TTL = 24 * time.Hour
	}
	if config.ReservationTTL <= 0 {
		config.ReservationTTL = 5 * time.Minute
	}
	if config.KeyGenerator == nil {
		config.KeyGenerator = GetIdempotencyKey
	}
	if config.Scope == nil {
		config.Scope = DefaultIdempotencyScope
	}

	skipSet := make(map[string]bool, len(config.SkipCommands))
	for _, t := range config.SkipCommands {
		skipSet[t] = true
	}

	return func(next MiddlewareFunc) MiddlewareFunc {
		return func(ctx context.Context, cmd Command) (CommandResult, error) {
			// Skip if command type is in skip list
			if skipSet[cmd.CommandType()] {
				return next(ctx, cmd)
			}

			// Derive the scoped, length-bounded idempotency key.
			key := EffectiveIdempotencyKey(cmd.CommandType(), config.Scope(ctx, cmd), config.KeyGenerator(cmd))
			if key == "" {
				// No key could be derived: no idempotency guarantee for this
				// command. Pass it through rather than dedupe it against a key
				// shared with unrelated commands.
				return next(ctx, cmd)
			}

			// Check if already processed.
			record, err := config.Store.Get(ctx, key)
			if err != nil {
				if config.FailClosed {
					return NewErrorResult(err), err
				}
				// Fail open: proceed without the idempotency guarantee.
				return next(ctx, cmd)
			}

			if record != nil {
				switch {
				case isProcessingRecord(record) && !record.IsExpired():
					// A concurrent command holds the key and is still in flight.
					return NewErrorResult(&IdempotencyReplayError{Key: key, Message: "command in progress"}), nil
				case !record.IsExpired():
					// Already processed: replay the stored result.
					return IdempotencyRecordToResult(record), nil
				default:
					// Stale record: remove it so the command can be reprocessed.
					_ = config.Store.Delete(ctx, key)
				}
			}

			// Reserve the key before executing so concurrent duplicates cannot
			// both run the handler.
			reserved := true
			reservation := newProcessingRecord(key, cmd.CommandType(), config.ReservationTTL)
			if ok, serr := config.Store.StoreIfAbsent(ctx, reservation); serr != nil {
				if config.FailClosed {
					return NewErrorResult(serr), serr
				}
				reserved = false // fail open: proceed without a reservation
			} else if !ok {
				// Lost the reservation race.
				if existing, gerr := config.Store.Get(ctx, key); gerr == nil && existing != nil &&
					!existing.IsExpired() && !isProcessingRecord(existing) {
					return IdempotencyRecordToResult(existing), nil
				}
				return NewErrorResult(&IdempotencyReplayError{Key: key, Message: "command in progress"}), nil
			}

			// Process command
			result, cmdErr := next(ctx, cmd)

			// Store result, or release the reservation if the result should not be kept.
			shouldStore := result.IsSuccess() || (config.StoreErrors && cmdErr != nil)
			if shouldStore {
				storeRecord := NewIdempotencyRecord(key, cmd.CommandType(), result, config.TTL)
				// Best effort - don't fail the command if store fails. If it does fail
				// while we hold a reservation, release the reservation so the stale
				// "processing" record doesn't block legitimate retries until the
				// reservation TTL expires.
				if serr := config.Store.Store(ctx, storeRecord); serr != nil && reserved {
					_ = config.Store.Delete(ctx, key)
				}
			} else if reserved {
				// Remove our reservation so the command can be retried.
				_ = config.Store.Delete(ctx, key)
			}

			return result, cmdErr
		}
	}
}

// IdempotencyKeyPrefix is a convenience function to create a prefixed idempotency key.
// When no key can be derived for the command (GetIdempotencyKey returns ""), the
// result is also "", so the bare prefix never becomes a key shared by every
// such command.
func IdempotencyKeyPrefix(prefix string) func(Command) string {
	return func(cmd Command) string {
		key := GetIdempotencyKey(cmd)
		if key == "" {
			return ""
		}
		return prefix + ":" + key
	}
}

// IdempotencyKeyFromField extracts the idempotency key from a field in the command.
// If the field is empty, it falls back to GenerateIdempotencyKey (which may
// itself return "" for a command that cannot be serialized).
func IdempotencyKeyFromField(fieldGetter func(Command) string) func(Command) string {
	return func(cmd Command) string {
		if key := fieldGetter(cmd); key != "" {
			return cmd.CommandType() + ":" + key
		}
		return GenerateIdempotencyKey(cmd)
	}
}

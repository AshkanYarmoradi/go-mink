package mink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Re-export types from adapters package for convenience
type (
	// IdempotencyStore tracks processed commands to prevent duplicate processing.
	IdempotencyStore = adapters.IdempotencyStore

	// IdempotencyRecord stores information about a processed command.
	IdempotencyRecord = adapters.IdempotencyRecord
)

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
func NewIdempotencyRecord(key, cmdType string, result CommandResult, ttl time.Duration) *IdempotencyRecord {
	now := time.Now()
	record := &IdempotencyRecord{
		Key:         key,
		CommandType: cmdType,
		AggregateID: result.AggregateID,
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
func GenerateIdempotencyKey(cmd Command) string {
	data, err := json.Marshal(cmd)
	if err != nil {
		// Fallback to a deterministic key based on the command type only.
		// This ensures identical commands produce identical keys even if serialization fails.
		typeHash := sha256.Sum256([]byte(cmd.CommandType()))
		return cmd.CommandType() + ":type-only:" + hex.EncodeToString(typeHash[:16])
	}

	hash := sha256.Sum256(data)
	return cmd.CommandType() + ":" + hex.EncodeToString(hash[:16])
}

// GetIdempotencyKey returns the idempotency key for a command.
// If the command implements IdempotentCommand, it uses that key.
// Otherwise, it generates a key from the command content.
func GetIdempotencyKey(cmd Command) string {
	if ic, ok := cmd.(IdempotentCommand); ok {
		return ic.IdempotencyKey()
	}
	return GenerateIdempotencyKey(cmd)
}

// IdempotencyConfig configures the idempotency middleware.
type IdempotencyConfig struct {
	// Store is the idempotency store to use.
	Store IdempotencyStore

	// TTL is how long to keep idempotency records.
	// Default is 24 hours.
	TTL time.Duration

	// KeyGenerator generates idempotency keys from commands.
	// If nil, GetIdempotencyKey is used.
	KeyGenerator func(Command) string

	// StoreErrors determines if failed commands should be stored.
	// If true, replaying a failed command returns the same error.
	// If false, failed commands can be retried.
	// Default is false.
	StoreErrors bool

	// SkipCommands is a list of command types to skip idempotency checking.
	SkipCommands []string
}

// DefaultIdempotencyConfig returns a default idempotency configuration.
func DefaultIdempotencyConfig(store IdempotencyStore) IdempotencyConfig {
	return IdempotencyConfig{
		Store:        store,
		TTL:          24 * time.Hour,
		KeyGenerator: GetIdempotencyKey,
		StoreErrors:  false,
		SkipCommands: nil,
	}
}

// IdempotencyMiddleware creates middleware that prevents duplicate command processing.
func IdempotencyMiddleware(config IdempotencyConfig) Middleware {
	if config.TTL <= 0 {
		config.TTL = 24 * time.Hour
	}
	if config.KeyGenerator == nil {
		config.KeyGenerator = GetIdempotencyKey
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

			// Generate idempotency key
			key := config.KeyGenerator(cmd)

			// Check if already processed
			record, err := config.Store.Get(ctx, key)
			if err != nil {
				// Store error - continue processing but log
				// In production, you might want to handle this differently
				return next(ctx, cmd)
			}

			if record != nil && !record.IsExpired() {
				// Already processed
				return IdempotencyRecordToResult(record), nil
			}

			// Process command
			result, cmdErr := next(ctx, cmd)

			// Store result
			shouldStore := result.IsSuccess() || (config.StoreErrors && cmdErr != nil)
			if shouldStore {
				storeRecord := NewIdempotencyRecord(key, cmd.CommandType(), result, config.TTL)
				// Best effort - don't fail the command if store fails
				_ = config.Store.Store(ctx, storeRecord)
			}

			return result, cmdErr
		}
	}
}

// IdempotencyKeyPrefix is a convenience function to create a prefixed idempotency key.
func IdempotencyKeyPrefix(prefix string) func(Command) string {
	return func(cmd Command) string {
		return prefix + ":" + GetIdempotencyKey(cmd)
	}
}

// IdempotencyKeyFromField extracts the idempotency key from a field in the command.
// If the field is empty, it falls back to GenerateIdempotencyKey.
func IdempotencyKeyFromField(fieldGetter func(Command) string) func(Command) string {
	return func(cmd Command) string {
		if key := fieldGetter(cmd); key != "" {
			return cmd.CommandType() + ":" + key
		}
		return GenerateIdempotencyKey(cmd)
	}
}

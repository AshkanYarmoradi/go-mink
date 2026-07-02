package adapters

import (
	"context"
	"time"
)

// AuditEntry is a single immutable record in the command audit trail.
// One entry is written per command dispatched through the audit middleware,
// capturing both successful and failed executions.
type AuditEntry struct {
	// ID is the unique identifier for this audit entry.
	ID string `json:"id"`

	// CommandType is the type of the audited command (e.g. "CreateOrder").
	CommandType string `json:"commandType"`

	// CommandID is the unique identifier of the command instance, if it
	// exposes one (via GetCommandID).
	CommandID string `json:"commandId,omitempty"`

	// AggregateID is the ID of the affected aggregate, if any.
	AggregateID string `json:"aggregateId,omitempty"`

	// Actor identifies who or what initiated the command.
	Actor string `json:"actor,omitempty"`

	// TenantID is the tenant the command was executed for, if any.
	TenantID string `json:"tenantId,omitempty"`

	// CorrelationID links related commands and events for distributed tracing.
	CorrelationID string `json:"correlationId,omitempty"`

	// CausationID identifies the event or command that caused this command.
	CausationID string `json:"causationId,omitempty"`

	// Error contains the error message if the command failed.
	Error string `json:"error,omitempty"`

	// Version is the aggregate version after processing, if any.
	Version int64 `json:"version,omitempty"`

	// DurationMs is how long the command took to execute, in milliseconds.
	DurationMs int64 `json:"durationMs,omitempty"`

	// Timestamp is when the command was audited.
	Timestamp time.Time `json:"timestamp"`

	// Success indicates if the command was processed successfully.
	Success bool `json:"success"`

	// Metadata contains arbitrary key-value pairs copied from the command.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// AuditOrder controls how audit entries are sorted when queried.
type AuditOrder int

const (
	// AuditOrderTimestampDesc returns the most recent entries first (default).
	AuditOrderTimestampDesc AuditOrder = iota

	// AuditOrderTimestampAsc returns the oldest entries first.
	AuditOrderTimestampAsc
)

// AuditQuery filters and paginates a query against the audit trail.
// Empty/zero fields are ignored, so a zero-value AuditQuery returns all entries
// (subject to the store's default limit).
type AuditQuery struct {
	// CommandType filters by exact command type.
	CommandType string

	// Actor filters by exact actor.
	Actor string

	// TenantID filters by exact tenant ID.
	TenantID string

	// AggregateID filters by exact aggregate ID.
	AggregateID string

	// CorrelationID filters by exact correlation ID (e.g. all commands in one request flow).
	CorrelationID string

	// From filters entries with Timestamp >= From (inclusive). Zero means no lower bound.
	From time.Time

	// To filters entries with Timestamp < To (exclusive). Zero means no upper bound.
	To time.Time

	// Success, when non-nil, filters by success/failure.
	Success *bool

	// Limit caps the number of returned entries. Limit <= 0 means no caller-imposed limit.
	Limit int

	// Offset skips the first Offset matching entries (for pagination).
	Offset int

	// Order controls the sort order of the returned entries.
	Order AuditOrder
}

// AuditStore persists and queries the command audit trail.
// Adapters may implement this to support audit logging.
type AuditStore interface {
	// Append writes a single audit entry. The store owns the persisted copy.
	Append(ctx context.Context, entry *AuditEntry) error

	// Find returns audit entries matching the query, honoring Order/Limit/Offset.
	Find(ctx context.Context, q AuditQuery) ([]*AuditEntry, error)

	// Count returns the number of entries matching the query.
	// It ignores Limit and Offset.
	Count(ctx context.Context, q AuditQuery) (int64, error)

	// Cleanup removes entries with a Timestamp older than olderThan ago.
	// Returns the number of entries removed.
	Cleanup(ctx context.Context, olderThan time.Duration) (int64, error)

	// Initialize prepares the store (e.g. creates tables). It must be safe to
	// call multiple times.
	Initialize(ctx context.Context) error

	// Close releases any resources held by the store.
	Close() error
}

// SubjectAuditPurger is an OPTIONAL AuditStore extension supporting GDPR erasure of a
// data subject's audit trail. Because the audit log records who/what/when in plaintext
// (Actor, TenantID, arbitrary Metadata, and raw Error strings that can carry PII),
// crypto-shredding the events does NOT reach it — a store must delete the subject's
// rows explicitly. Stores MAY implement it; mink.NewAuditSubjectEraser detects support
// and skips (rather than fails) when absent.
type SubjectAuditPurger interface {
	// DeleteAuditBySubject removes audit entries attributable to subjectID — those
	// whose Actor or AggregateID equals subjectID — and returns the count removed.
	DeleteAuditBySubject(ctx context.Context, subjectID string) (int64, error)
}

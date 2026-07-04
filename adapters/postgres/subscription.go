package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"go-mink.dev/adapters"
)

// Default values for subscriptions.
const (
	defaultPollInterval       = 100 * time.Millisecond
	defaultSubscriptionBuffer = 100
	defaultMaxRetries         = 5
)

// subscriptionConfig holds the resolved configuration for a subscription.
type subscriptionConfig struct {
	bufferSize   int
	pollInterval time.Duration
	maxRetries   int
	onError      func(err error)
}

// newSubscriptionConfig creates a config from options with defaults applied.
func newSubscriptionConfig(opts []adapters.SubscriptionOptions) subscriptionConfig {
	cfg := subscriptionConfig{
		bufferSize:   defaultSubscriptionBuffer,
		pollInterval: defaultPollInterval,
		maxRetries:   defaultMaxRetries,
	}

	if len(opts) > 0 {
		opt := opts[0]
		if opt.BufferSize > 0 {
			cfg.bufferSize = opt.BufferSize
		}
		if opt.PollInterval > 0 {
			cfg.pollInterval = opt.PollInterval
		}
		cfg.onError = opt.OnError
	}

	return cfg
}

// handleError calls the error handler or logs the error.
func (c *subscriptionConfig) handleError(err error, context string) {
	if c.onError != nil {
		c.onError(err)
	} else {
		log.Printf("mink/postgres: subscription error (%s): %v", context, err)
	}
}

// eventLoader is a function that loads events and returns the new position.
// The position type is generic to support both uint64 (global position) and int64 (version).
type eventLoader[P any] func(ctx context.Context, position P) ([]adapters.StoredEvent, P, error)

// runPollingLoop runs a generic polling loop for subscriptions.
// It handles error retries, context cancellation, and event delivery.
func runPollingLoop[P any](
	ctx context.Context,
	cfg subscriptionConfig,
	ch chan adapters.StoredEvent,
	errorContext string,
	initialPosition P,
	loader eventLoader[P],
) {
	defer close(ch)
	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()

	currentPosition := initialPosition
	consecutiveErrors := 0

	for {
		// Check for shutdown before polling
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Load events from current position
		events, newPosition, err := loader(ctx, currentPosition)
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= cfg.maxRetries {
				cfg.handleError(err, errorContext)
				consecutiveErrors = 0 // Reset after logging
			}
			// On error, wait and retry (unless context is cancelled)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}
		consecutiveErrors = 0

		// Send events to channel
		for _, event := range events {
			select {
			case ch <- event:
				// Event delivered successfully
			case <-ctx.Done():
				return
			}
		}

		// Update position only after entire batch is successfully delivered
		// This ensures no events are skipped if context is cancelled mid-batch
		if len(events) > 0 {
			currentPosition = newPosition
		}

		// Wait for next poll cycle if no events
		if len(events) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Continue polling
			}
		}
	}
}

// safePositionClause restricts a load-from-position query to the gapless "safe"
// watermark: only rows whose inserting transaction is older than every transaction still
// in flight. global_position (BIGSERIAL) is assigned at INSERT but a row becomes visible
// only at COMMIT, so a naive `global_position > cursor` poll can advance past a lower
// position whose transaction commits later, permanently skipping it (and, for async
// projections/sagas, checkpointing past it). This clause holds the cursor back until such
// a lower-positioned transaction commits or aborts, so subscriptions, projections, and
// sagas never skip a committed event. At-least-once is preserved; only at-most-once loss
// is removed.
//
// Robustness: age() makes the xid comparison wraparound-safe; a read-only transaction
// never holds a real xid, so long reporting queries / pg_dump do NOT stall delivery — only
// an in-flight *write* transaction does (append transactions are short). Known narrow
// residual: a concurrent first-event of two *new* streams has a microsecond window where a
// transaction's xid and global_position can be assigned out of order (existing-stream
// appends assign both atomically at the events INSERT and are always safe). A long-running
// unrelated *write* transaction in the same database can delay delivery until it ends.
const safePositionClause = ` AND age(xmin) > age(pg_snapshot_xmin(pg_current_snapshot())::text::xid)`

// positionScanQuery builds the shared "load events after $1, up to the gapless safe watermark"
// SELECT used by every position-based read path (SubscribeAll, categories, async projections,
// the saga manager). Routing all callers through one builder guarantees none can omit
// safePositionClause — dropping it silently reintroduces the at-most-once skip it exists to
// prevent. extraWhere is appended inside the WHERE (e.g. " AND stream_id LIKE $2"); limitParam
// is the LIMIT placeholder ("$2" with no extraWhere, "$3" with one).
func positionScanQuery(schemaQ, extraWhere, limitParam string) string {
	return `
		SELECT event_id, stream_id, version, event_type, data, metadata, global_position, timestamp
		FROM ` + schemaQ + `.events
		WHERE global_position > $1` + safePositionClause + extraWhere + `
		ORDER BY global_position ASC
		LIMIT ` + limitParam
}

// LoadFromPosition loads events starting from a global position, up to the gapless safe
// watermark (see safePositionClause). This is the shared read path for SubscribeAll,
// async projections, and the saga manager, so all three inherit the no-skip guarantee.
func (a *PostgresAdapter) LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]adapters.StoredEvent, error) {
	if a.closed.Load() {
		return nil, ErrAdapterClosed
	}

	limit = adapters.DefaultLimit(limit, 1000)

	query := positionScanQuery(a.schemaQ, "", "$2")

	rows, err := a.db.QueryContext(ctx, query, fromPosition, limit)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to load events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return a.scanEvents(rows)
}

// SubscribeAll subscribes to all events across all streams.
// Events are delivered starting from the specified global position.
// This uses polling-based subscription with continuous updates.
// Optional SubscriptionOptions can be provided to configure behavior.
func (a *PostgresAdapter) SubscribeAll(ctx context.Context, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if a.closed.Load() {
		return nil, ErrAdapterClosed
	}

	cfg := newSubscriptionConfig(opts)
	ch := make(chan adapters.StoredEvent, cfg.bufferSize)

	loader := func(ctx context.Context, position uint64) ([]adapters.StoredEvent, uint64, error) {
		events, err := a.LoadFromPosition(ctx, position, cfg.bufferSize)
		if err != nil {
			return nil, position, err
		}
		newPosition := position
		if len(events) > 0 {
			newPosition = events[len(events)-1].GlobalPosition
		}
		return events, newPosition, nil
	}

	go runPollingLoop(ctx, cfg, ch, "SubscribeAll", fromPosition, loader)

	return ch, nil
}

// SubscribeStream subscribes to events from a specific stream.
// Events are delivered starting from the specified version with continuous polling.
// Optional SubscriptionOptions can be provided to configure behavior.
func (a *PostgresAdapter) SubscribeStream(ctx context.Context, streamID string, fromVersion int64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if a.closed.Load() {
		return nil, ErrAdapterClosed
	}

	cfg := newSubscriptionConfig(opts)
	ch := make(chan adapters.StoredEvent, cfg.bufferSize)

	loader := func(ctx context.Context, version int64) ([]adapters.StoredEvent, int64, error) {
		events, err := a.Load(ctx, streamID, version)
		if err != nil {
			return nil, version, err
		}
		newVersion := version
		if len(events) > 0 {
			newVersion = events[len(events)-1].Version
		}
		return events, newVersion, nil
	}

	go runPollingLoop(ctx, cfg, ch, fmt.Sprintf("SubscribeStream(%s)", streamID), fromVersion, loader)

	return ch, nil
}

// SubscribeCategory subscribes to all events from streams in a category.
// Events are delivered starting from the specified global position with continuous polling.
// Optional SubscriptionOptions can be provided to configure behavior.
func (a *PostgresAdapter) SubscribeCategory(ctx context.Context, category string, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if a.closed.Load() {
		return nil, ErrAdapterClosed
	}

	cfg := newSubscriptionConfig(opts)
	ch := make(chan adapters.StoredEvent, cfg.bufferSize)

	loader := func(ctx context.Context, position uint64) ([]adapters.StoredEvent, uint64, error) {
		events, err := a.loadCategoryEvents(ctx, category, position, cfg.bufferSize)
		if err != nil {
			return nil, position, err
		}
		newPosition := position
		if len(events) > 0 {
			newPosition = events[len(events)-1].GlobalPosition
		}
		return events, newPosition, nil
	}

	go runPollingLoop(ctx, cfg, ch, fmt.Sprintf("SubscribeCategory(%s)", category), fromPosition, loader)

	return ch, nil
}

// loadCategoryEvents loads events for a specific category from a position.
func (a *PostgresAdapter) loadCategoryEvents(ctx context.Context, category string, fromPosition uint64, limit int) ([]adapters.StoredEvent, error) {
	query := positionScanQuery(a.schemaQ, " AND stream_id LIKE $2", "$3")

	// Escape LIKE metacharacters in the category so a name containing % or _ matches
	// only its own streams (the trailing -% stays the intended wildcard).
	rows, err := a.db.QueryContext(ctx, query, fromPosition, escapeLikePattern(category)+"-%", limit)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to load category events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return a.scanEvents(rows)
}

// scanEvents scans rows into StoredEvent slice.
func (a *PostgresAdapter) scanEvents(rows *sql.Rows) ([]adapters.StoredEvent, error) {
	var events []adapters.StoredEvent

	for rows.Next() {
		var event adapters.StoredEvent
		var metadataJSON []byte

		err := rows.Scan(
			&event.ID,
			&event.StreamID,
			&event.Version,
			&event.Type,
			&event.Data,
			&metadataJSON,
			&event.GlobalPosition,
			&event.Timestamp,
		)
		if err != nil {
			return nil, fmt.Errorf("mink/postgres: failed to scan event: %w", err)
		}

		if len(metadataJSON) > 0 {
			if err := parseMetadata(metadataJSON, &event.Metadata); err != nil {
				return nil, err
			}
		}

		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mink/postgres: row iteration error: %w", err)
	}

	return events, nil
}

// parseMetadata parses JSON metadata into the Metadata struct.
//
// It first decodes the library's own shape (string custom values). Metadata not
// written by this library — e.g. a custom object with non-string values — is
// handled by a tolerant fallback that stringifies custom values rather than
// failing the whole Load. Only genuinely-invalid (unparseable) JSON is surfaced
// as an error, so one odd row does not silently drop fields nor block a batch.
func parseMetadata(data []byte, m *adapters.Metadata) error {
	if len(data) == 0 || string(data) == "{}" {
		return nil
	}

	var meta struct {
		CorrelationID string            `json:"correlationId"`
		CausationID   string            `json:"causationId"`
		UserID        string            `json:"userId"`
		TenantID      string            `json:"tenantId"`
		Custom        map[string]string `json:"custom"`
	}
	if err := json.Unmarshal(data, &meta); err == nil {
		m.CorrelationID = meta.CorrelationID
		m.CausationID = meta.CausationID
		m.UserID = meta.UserID
		m.TenantID = meta.TenantID
		m.Custom = meta.Custom
		return nil
	}

	// Tolerant fallback: decode custom values as arbitrary JSON and stringify them.
	var loose struct {
		CorrelationID string                 `json:"correlationId"`
		CausationID   string                 `json:"causationId"`
		UserID        string                 `json:"userId"`
		TenantID      string                 `json:"tenantId"`
		Custom        map[string]interface{} `json:"custom"`
	}
	if err := json.Unmarshal(data, &loose); err != nil {
		return fmt.Errorf("mink/postgres: failed to unmarshal event metadata: %w", err)
	}
	m.CorrelationID = loose.CorrelationID
	m.CausationID = loose.CausationID
	m.UserID = loose.UserID
	m.TenantID = loose.TenantID
	if len(loose.Custom) > 0 {
		m.Custom = make(map[string]string, len(loose.Custom))
		for k, v := range loose.Custom {
			if s, ok := v.(string); ok {
				m.Custom[k] = s
				continue
			}
			b, _ := json.Marshal(v)
			m.Custom[k] = string(b)
		}
	}
	return nil
}

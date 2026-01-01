package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
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

// LoadFromPosition loads events starting from a global position.
// This is used by projection engines to catch up on historical events.
func (a *PostgresAdapter) LoadFromPosition(ctx context.Context, fromPosition uint64, limit int) ([]adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	if limit <= 0 {
		limit = 1000
	}

	query := fmt.Sprintf(`
		SELECT event_id, stream_id, version, event_type, data, metadata, global_position, timestamp
		FROM %s.events
		WHERE global_position > $1
		ORDER BY global_position ASC
		LIMIT $2`, a.schema)

	rows, err := a.db.QueryContext(ctx, query, fromPosition, limit)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to load events: %w", err)
	}
	defer rows.Close()

	return a.scanEvents(rows)
}

// SubscribeAll subscribes to all events across all streams.
// Events are delivered starting from the specified global position.
// This uses polling-based subscription with continuous updates.
// Optional SubscriptionOptions can be provided to configure behavior.
func (a *PostgresAdapter) SubscribeAll(ctx context.Context, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	cfg := newSubscriptionConfig(opts)
	ch := make(chan adapters.StoredEvent, cfg.bufferSize)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(cfg.pollInterval)
		defer ticker.Stop()

		currentPosition := fromPosition
		consecutiveErrors := 0

		for {
			// Check for shutdown before polling
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Load events from current position
			events, err := a.LoadFromPosition(ctx, currentPosition, cfg.bufferSize)
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors >= cfg.maxRetries {
					cfg.handleError(err, "SubscribeAll")
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
					currentPosition = event.GlobalPosition
				case <-ctx.Done():
					return
				}
			}

			// Wait for next poll cycle if no events, otherwise continue immediately
			if len(events) == 0 {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					// Continue polling
				}
			}
		}
	}()

	return ch, nil
}

// SubscribeStream subscribes to events from a specific stream.
// Events are delivered starting from the specified version with continuous polling.
// Optional SubscriptionOptions can be provided to configure behavior.
func (a *PostgresAdapter) SubscribeStream(ctx context.Context, streamID string, fromVersion int64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	cfg := newSubscriptionConfig(opts)
	ch := make(chan adapters.StoredEvent, cfg.bufferSize)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(cfg.pollInterval)
		defer ticker.Stop()

		currentVersion := fromVersion
		consecutiveErrors := 0

		for {
			// Check for shutdown before polling
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Load events from current version
			events, err := a.Load(ctx, streamID, currentVersion)
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors >= cfg.maxRetries {
					cfg.handleError(err, fmt.Sprintf("SubscribeStream(%s)", streamID))
					consecutiveErrors = 0
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
					currentVersion = event.Version + 1
				case <-ctx.Done():
					return
				}
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
	}()

	return ch, nil
}

// SubscribeCategory subscribes to all events from streams in a category.
// Events are delivered starting from the specified global position with continuous polling.
// Optional SubscriptionOptions can be provided to configure behavior.
func (a *PostgresAdapter) SubscribeCategory(ctx context.Context, category string, fromPosition uint64, opts ...adapters.SubscriptionOptions) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	cfg := newSubscriptionConfig(opts)
	ch := make(chan adapters.StoredEvent, cfg.bufferSize)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(cfg.pollInterval)
		defer ticker.Stop()

		currentPosition := fromPosition
		consecutiveErrors := 0

		for {
			// Check for shutdown before polling
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Load events for this category
			events, err := a.loadCategoryEvents(ctx, category, currentPosition, cfg.bufferSize)
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors >= cfg.maxRetries {
					cfg.handleError(err, fmt.Sprintf("SubscribeCategory(%s)", category))
					consecutiveErrors = 0
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
					currentPosition = event.GlobalPosition
				case <-ctx.Done():
					return
				}
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
	}()

	return ch, nil
}

// loadCategoryEvents loads events for a specific category from a position.
func (a *PostgresAdapter) loadCategoryEvents(ctx context.Context, category string, fromPosition uint64, limit int) ([]adapters.StoredEvent, error) {
	query := fmt.Sprintf(`
		SELECT event_id, stream_id, version, event_type, data, metadata, global_position, timestamp
		FROM %s.events
		WHERE global_position > $1 AND stream_id LIKE $2
		ORDER BY global_position ASC
		LIMIT $3`, a.schema)

	rows, err := a.db.QueryContext(ctx, query, fromPosition, category+"-%", limit)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: failed to load category events: %w", err)
	}
	defer rows.Close()

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

	if err := json.Unmarshal(data, &meta); err != nil {
		// Ignore unmarshal errors - metadata may have different structure
		return nil
	}

	m.CorrelationID = meta.CorrelationID
	m.CausationID = meta.CausationID
	m.UserID = meta.UserID
	m.TenantID = meta.TenantID
	m.Custom = meta.Custom

	return nil
}

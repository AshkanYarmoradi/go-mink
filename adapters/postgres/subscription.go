package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

// Default polling interval for subscriptions.
const defaultPollInterval = 100 * time.Millisecond

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
func (a *PostgresAdapter) SubscribeAll(ctx context.Context, fromPosition uint64) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	ch := make(chan adapters.StoredEvent, 100)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(defaultPollInterval)
		defer ticker.Stop()

		currentPosition := fromPosition

		for {
			// Check for shutdown before polling
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Load events from current position
			events, err := a.LoadFromPosition(ctx, currentPosition, 100)
			if err != nil {
				// On error, wait and retry (unless context is cancelled)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					continue
				}
			}

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
func (a *PostgresAdapter) SubscribeStream(ctx context.Context, streamID string, fromVersion int64) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	ch := make(chan adapters.StoredEvent, 100)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(defaultPollInterval)
		defer ticker.Stop()

		currentVersion := fromVersion

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
				// On error, wait and retry (unless context is cancelled)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					continue
				}
			}

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
func (a *PostgresAdapter) SubscribeCategory(ctx context.Context, category string, fromPosition uint64) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	ch := make(chan adapters.StoredEvent, 100)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(defaultPollInterval)
		defer ticker.Stop()

		currentPosition := fromPosition

		for {
			// Check for shutdown before polling
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Load events for this category
			events, err := a.loadCategoryEvents(ctx, category, currentPosition, 100)
			if err != nil {
				// On error, wait and retry (unless context is cancelled)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					continue
				}
			}

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

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AshkanYarmoradi/go-mink/adapters"
)

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
		SELECT id, stream_id, version, type, data, metadata, global_position, timestamp
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
// This uses polling-based subscription.
func (a *PostgresAdapter) SubscribeAll(ctx context.Context, fromPosition uint64) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	ch := make(chan adapters.StoredEvent, 100)

	go func() {
		defer close(ch)

		// Send historical events first
		events, err := a.LoadFromPosition(ctx, fromPosition, 1000)
		if err != nil {
			return
		}

		for _, event := range events {
			select {
			case ch <- event:
				fromPosition = event.GlobalPosition
			case <-ctx.Done():
				return
			}
		}

		// Note: For production use, you'd want to use LISTEN/NOTIFY
		// or polling for real-time updates. This basic implementation
		// only sends historical events.
	}()

	return ch, nil
}

// SubscribeStream subscribes to events from a specific stream.
// Events are delivered starting from the specified version.
func (a *PostgresAdapter) SubscribeStream(ctx context.Context, streamID string, fromVersion int64) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	ch := make(chan adapters.StoredEvent, 100)

	go func() {
		defer close(ch)

		// Load historical events for this stream
		events, err := a.Load(ctx, streamID, fromVersion)
		if err != nil {
			return
		}

		for _, event := range events {
			select {
			case ch <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

// SubscribeCategory subscribes to all events from streams in a category.
// Events are delivered starting from the specified global position.
func (a *PostgresAdapter) SubscribeCategory(ctx context.Context, category string, fromPosition uint64) (<-chan adapters.StoredEvent, error) {
	if a.closed {
		return nil, ErrAdapterClosed
	}

	ch := make(chan adapters.StoredEvent, 100)

	go func() {
		defer close(ch)

		query := fmt.Sprintf(`
			SELECT id, stream_id, version, type, data, metadata, global_position, timestamp
			FROM %s.events
			WHERE global_position > $1 AND stream_id LIKE $2
			ORDER BY global_position ASC
			LIMIT 1000`, a.schema)

		rows, err := a.db.QueryContext(ctx, query, fromPosition, category+"-%")
		if err != nil {
			return
		}
		defer rows.Close()

		events, err := a.scanEvents(rows)
		if err != nil {
			return
		}

		for _, event := range events {
			select {
			case ch <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
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

// parseMetadata parses JSON metadata.
func parseMetadata(data []byte, m *adapters.Metadata) error {
	if len(data) == 0 || string(data) == "{}" {
		return nil
	}
	// Simple parsing - just extract known fields
	// A more robust implementation would use json.Unmarshal
	s := string(data)
	if strings.Contains(s, "correlationId") || strings.Contains(s, "causationId") ||
		strings.Contains(s, "userId") || strings.Contains(s, "tenantId") {
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
		}
	}
	return nil
}

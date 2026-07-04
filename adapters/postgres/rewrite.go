package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"go-mink.dev/adapters"
)

var _ adapters.EventRewriteAdapter = (*PostgresAdapter)(nil)

// RewriteEventData replaces the data and metadata of the event at (streamID,
// version) in place, preserving all other columns. It is the persistence half of
// EventStore.ReEncryptStreamInPlace — an in-place, data-governance write outside the
// append path (see adapters.EventRewriteAdapter). Errors if no such event exists.
func (a *PostgresAdapter) RewriteEventData(ctx context.Context, streamID string, version int64, data []byte, metadata adapters.Metadata) error {
	if a.closed.Load() {
		return ErrAdapterClosed
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to marshal metadata: %w", err)
	}
	res, err := a.db.ExecContext(ctx,
		`UPDATE `+a.schemaQ+`.events SET data = $1, metadata = $2 WHERE stream_id = $3 AND version = $4`,
		data, metadataJSON, streamID, version)
	if err != nil {
		return fmt.Errorf("mink/postgres: failed to rewrite event data: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mink/postgres: rewrite rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("mink/postgres: no event at stream %q version %d to rewrite", streamID, version)
	}
	return nil
}

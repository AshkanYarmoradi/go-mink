package postgres

import (
	"context"
	"fmt"

	mink "go-mink.dev"
)

// PostgresAdapter implements mink.SubjectIndexAdapter, resolving a subject's streams
// directly from the events' own tags — a drift-free index (it reads the source of truth,
// not a separately-maintained table, so it cannot fall out of sync).
var _ mink.SubjectIndexAdapter = (*PostgresAdapter)(nil)

// StreamsBySubject returns the distinct streams containing events tagged for subjectID,
// by querying the events' subject tags in JSONB (metadata->'custom'->>'$subjects'). It is
// drift-free — unlike a separate SubjectIndex table it reads the events table itself — so
// it is safe to treat as authoritative for the TAGGED events. (It cannot see legacy
// UNtagged events, so a resolver still marks the footprint Partial unless you pass
// mink.WithAuthoritativeIndex.) Implements mink.SubjectIndexAdapter; inject it with
// mink.WithResolverIndex(adapter) for O(the subject's streams), DB-side resolution.
//
// For large stores add a GIN expression index so this is not a sequential scan:
//
//	CREATE INDEX CONCURRENTLY idx_events_subjects
//	  ON <schema>.events USING gin (((metadata->'custom'->>'$subjects')::jsonb));
func (a *PostgresAdapter) StreamsBySubject(ctx context.Context, subjectID string) ([]string, error) {
	// jsonb_exists is the function form of the `?` operator (avoids `?` being mistaken
	// for a bind placeholder); it tests membership in the $subjects JSON array. Rows
	// without the tag yield NULL and are excluded.
	query := `SELECT DISTINCT stream_id FROM ` + a.schemaQ + `.events
		WHERE metadata->'custom'->>'` + mink.SubjectTagsKey + `' IS NOT NULL
		  AND jsonb_exists((metadata->'custom'->>'` + mink.SubjectTagsKey + `')::jsonb, $1)
		ORDER BY stream_id`

	rows, err := a.db.QueryContext(ctx, query, subjectID)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: streams by subject %q: %w", subjectID, err)
	}
	defer func() { _ = rows.Close() }()

	var streams []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("mink/postgres: scan stream for subject %q: %w", subjectID, err)
		}
		streams = append(streams, s)
	}
	return streams, rows.Err()
}

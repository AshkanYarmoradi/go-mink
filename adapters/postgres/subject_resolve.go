package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	mink "go-mink.dev"
)

// PostgresAdapter implements mink.SubjectIndexAdapter, resolving a subject's streams
// directly from the events' own tags — a drift-free index (it reads the source of truth,
// not a separately-maintained table, so it cannot fall out of sync).
var _ mink.SubjectIndexAdapter = (*PostgresAdapter)(nil)

// pgInvalidTextRepresentation is SQLState 22P02 (invalid_text_representation), which
// PostgreSQL raises when a text value cannot be cast to the requested type — here, a
// malformed $subjects tag failing the ::jsonb cast.
const pgInvalidTextRepresentation = "22P02"

// ErrSubjectTagMalformed is the sentinel behind *SubjectTagMalformedError:
// StreamsBySubject returns it when its Go-side fallback scan meets a row whose
// $subjects tag cannot be parsed AND whose raw tag text contains the subject id —
// a row that might name the subject. Dropping such a row would make the result
// silently partial, and an erasure footprint must never be silently partial, so
// resolution fails loudly instead. Repair (or re-tag) the offending rows and retry.
// The error carries counts only — never the raw tag text or the stream ids.
var ErrSubjectTagMalformed = errors.New("mink/postgres: malformed $subjects tag may name the subject")

// SubjectTagMalformedError reports how many unparseable $subjects tags mention the
// subject being resolved. It wraps ErrSubjectTagMalformed (errors.Is) and is PII-free
// by construction: it names the subject the caller asked about and carries counts,
// never the malformed tag text or the stream ids holding it.
type SubjectTagMalformedError struct {
	// SubjectID is the subject whose resolution was refused.
	SubjectID string
	// Rows is the number of malformed tag rows whose raw text contains SubjectID.
	Rows int
	// Streams is the number of distinct streams those rows belong to, none of which
	// was resolved for the subject through a well-formed row.
	Streams int
}

// Error implements error.
func (e *SubjectTagMalformedError) Error() string {
	return fmt.Sprintf("mink/postgres: streams by subject %q: %d malformed $subjects tag row(s) in %d unresolved stream(s) contain the subject id and cannot be parsed; the footprint would be incomplete, repair the tags and retry",
		e.SubjectID, e.Rows, e.Streams)
}

// Is reports whether target is ErrSubjectTagMalformed, for errors.Is.
func (e *SubjectTagMalformedError) Is(target error) bool { return target == ErrSubjectTagMalformed }

// Unwrap returns ErrSubjectTagMalformed.
func (e *SubjectTagMalformedError) Unwrap() error { return ErrSubjectTagMalformed }

// sqlState returns the SQLSTATE code carried by err (or anything it wraps), or an
// empty string. It relies on the SQLState() accessor that both pgx (*pgconn.PgError)
// and lib/pq (*pq.Error) expose, so it is driver-agnostic.
func sqlState(err error) string {
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		return coded.SQLState()
	}
	return ""
}

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
//
// Ordering: both paths below de-duplicate and sort the result bytewise in Go
// (sort.Strings), so the order never depends on the column collation and the
// fallback returns exactly what the fast path returns for the same data.
//
// Resilience: the fast path casts every tagged row's $subjects value to jsonb, so a
// single malformed tag on ANY row (one that is not even this subject's) would make the
// whole statement — and therefore resolution for every subject — fail. When PostgreSQL
// reports that cast failure (SQLSTATE 22P02) the adapter falls back to reading the raw
// tag text and parsing it in Go. A malformed row whose raw text does not contain the
// subject id (verbatim or in its JSON-escaped spelling) cannot name the subject and is
// skipped. A malformed row that DOES contain the id, on a stream the subject was not
// resolved to through a well-formed row, might name the subject: then the call fails
// with a *SubjectTagMalformedError (errors.Is ErrSubjectTagMalformed) instead of
// returning a silently partial footprint. For well-formed data the fallback returns
// exactly the streams the fast path would; it is only slower (a scan of the tagged
// rows), and it is never used while the data is healthy.
func (a *PostgresAdapter) StreamsBySubject(ctx context.Context, subjectID string) ([]string, error) {
	streams, err := a.streamsBySubjectJSONB(ctx, subjectID)
	if err == nil {
		return streams, nil
	}
	if sqlState(err) != pgInvalidTextRepresentation {
		return nil, err
	}
	return a.streamsBySubjectScan(ctx, subjectID)
}

// streamsBySubjectJSONB is the DB-side fast path: membership is tested by PostgreSQL
// on the tag cast to jsonb (index-assisted when the GIN expression index exists).
// The result is sorted in Go, not by the database, so it matches the fallback.
func (a *PostgresAdapter) streamsBySubjectJSONB(ctx context.Context, subjectID string) ([]string, error) {
	// jsonb_exists is the function form of the `?` operator (avoids `?` being mistaken
	// for a bind placeholder); it tests membership in the $subjects JSON array. Rows
	// without the tag yield NULL and are excluded.
	query := `SELECT DISTINCT stream_id FROM ` + a.schemaQ + `.events
		WHERE metadata->'custom'->>'` + mink.SubjectTagsKey + `' IS NOT NULL
		  AND jsonb_exists((metadata->'custom'->>'` + mink.SubjectTagsKey + `')::jsonb, $1)`

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
	if err := rows.Err(); err != nil {
		// A malformed tag can surface during iteration rather than at QueryContext,
		// so wrap here too for the caller's SQLSTATE check.
		return nil, fmt.Errorf("mink/postgres: streams by subject %q: %w", subjectID, err)
	}
	sort.Strings(streams)
	return streams, nil
}

// streamsBySubjectScan is the tolerant fallback: it reads each tagged row's raw tag
// text and decides membership in Go. A row whose tag is not a JSON array of strings
// is skipped when its text cannot mention the subject, and is fatal (see
// ErrSubjectTagMalformed) when it might — unless the same stream was resolved for the
// subject through a well-formed row anyway, in which case nothing was lost. Results
// are de-duplicated and sorted bytewise exactly like the fast path.
func (a *PostgresAdapter) streamsBySubjectScan(ctx context.Context, subjectID string) ([]string, error) {
	tagExpr := `metadata->'custom'->>'` + mink.SubjectTagsKey + `'`
	query := `SELECT stream_id, ` + tagExpr + ` FROM ` + a.schemaQ + `.events
		WHERE ` + tagExpr + ` IS NOT NULL`
	var args []interface{}
	if appearsVerbatimInJSON(subjectID) {
		// Cheap server-side pre-filter: when no JSON encoder would escape any byte of
		// the id, a well-formed tag naming the subject must contain the id verbatim,
		// so rows that do not can be skipped before they cross the wire. It is only a
		// necessary condition; exact membership is still decided below. (A malformed
		// row skipped here cannot mention the subject either, by the same argument.)
		query += ` AND strpos(` + tagExpr + `, $1) > 0`
		args = append(args, subjectID)
	}

	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres: streams by subject %q (scan): %w", subjectID, err)
	}
	defer func() { _ = rows.Close() }()

	seen := make(map[string]struct{}) // streams resolved through a well-formed tag
	suspect := make(map[string]int)   // stream -> malformed rows whose text mentions the subject
	var streams []string
	for rows.Next() {
		var streamID, raw string
		if err := rows.Scan(&streamID, &raw); err != nil {
			return nil, fmt.Errorf("mink/postgres: scan stream for subject %q (scan): %w", subjectID, err)
		}
		if _, done := seen[streamID]; done {
			continue
		}
		var tags []string
		if json.Unmarshal([]byte(raw), &tags) != nil {
			if tagMayName(raw, subjectID) {
				suspect[streamID]++
			}
			continue // malformed and silent about this subject: it cannot name it
		}
		if slices.Contains(tags, subjectID) {
			seen[streamID] = struct{}{}
			streams = append(streams, streamID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mink/postgres: streams by subject %q (scan): %w", subjectID, err)
	}

	// A malformed row only loses information when its stream was not resolved for the
	// subject anyway; count the rest and refuse rather than answer partially.
	var badRows, badStreams int
	for streamID, n := range suspect {
		if _, resolved := seen[streamID]; resolved {
			continue
		}
		badRows += n
		badStreams++
	}
	if badStreams > 0 {
		return nil, &SubjectTagMalformedError{SubjectID: subjectID, Rows: badRows, Streams: badStreams}
	}
	sort.Strings(streams)
	return streams, nil
}

// tagMayName reports whether the raw text of a malformed $subjects tag might name
// subjectID: it contains the id verbatim, or in the JSON-escaped spelling a corrupted
// (truncated, mis-quoted) but originally well-formed tag would carry. It is a
// deliberately conservative necessary condition — a false positive fails loudly and is
// repaired, a false negative would be a silent hole in an erasure footprint.
func tagMayName(raw, subjectID string) bool {
	if strings.Contains(raw, subjectID) {
		return true
	}
	enc, err := json.Marshal(subjectID)
	if err != nil || len(enc) < 2 {
		return false
	}
	escaped := string(enc[1 : len(enc)-1]) // strip the surrounding quotes
	return escaped != subjectID && strings.Contains(raw, escaped)
}

// appearsVerbatimInJSON reports whether s is non-empty and consists only of bytes that
// no JSON encoder escapes, so the JSON encoding of any string containing s also
// contains s byte-for-byte. Besides the JSON-mandated escapes ('"', '\', control
// characters) it excludes the optional escapes some encoders apply ('<', '>', '&', the
// apostrophe and '/') and all non-ASCII bytes (which may be emitted as backslash-u
// escapes).
func appearsVerbatimInJSON(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			return false
		}
		switch c {
		case '"', '\\', '<', '>', '&', '\'', '/':
			return false
		}
	}
	return true
}

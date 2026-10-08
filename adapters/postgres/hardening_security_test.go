package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mink "go-mink.dev"
	"go-mink.dev/adapters"
)

// ---------------------------------------------------------------------------
// GenerateSchema: project name sanitization
// ---------------------------------------------------------------------------

func TestGenerateSchema_HostileProjectNameCannotInjectDDL(t *testing.T) {
	const injected = `DROP TABLE "mink"."events"`
	hostile := "proj\n-- end\n" + injected + "; --'\r\n SELECT 1;"

	hostileOut := GenerateSchema(hostile, "mink", "events", "snapshots", "mink_outbox")
	benignOut := GenerateSchema("proj", "mink", "events", "snapshots", "mink_outbox")

	hostileLines := strings.Split(hostileOut, "\n")
	benignLines := strings.Split(benignOut, "\n")
	require.Equal(t, len(benignLines), len(hostileLines), "a hostile project name must not add lines (and so statements) to the DDL")

	// The whole hostile name is confined to the single header comment line ...
	assert.Equal(t, `-- Generated for: proj-- end`+injected+`; --''SELECT 1;`, hostileLines[1])
	// ... and everything after it is byte-identical to the benign output.
	assert.Equal(t, benignLines[2:], hostileLines[2:])
	for i, line := range hostileLines {
		if i == 1 {
			continue
		}
		assert.NotContains(t, line, "DROP TABLE", "line %d", i)
		assert.NotContains(t, line, "SELECT 1", "line %d", i)
	}
	assertRuntimeSchemaDDL(t, hostileOut)
}

// ---------------------------------------------------------------------------
// ListStreams: LIKE metacharacters in the prefix
// ---------------------------------------------------------------------------

func TestPostgresAdapter_ListStreams_EscapesLikeMetacharacters(t *testing.T) {
	adapter := setupIntegrationTest(t)
	ctx := context.Background()
	for _, id := range []string{"Order%-1", "OrderX-1", "Order_-1", `Order\-1`, "Order-1"} {
		_, err := adapter.Append(ctx, id, []adapters.EventRecord{{Type: "E", Data: []byte(`{}`)}}, mink.NoStream)
		require.NoError(t, err)
	}
	list := func(prefix string) []string {
		t.Helper()
		summaries, err := adapter.ListStreams(ctx, prefix, 100)
		require.NoError(t, err)
		ids := make([]string, 0, len(summaries))
		for _, s := range summaries {
			ids = append(ids, s.StreamID)
		}
		return ids
	}

	// Unescaped, "Order%" would become the pattern Order%% and match all five.
	assert.Equal(t, []string{"Order%-1"}, list("Order%"))
	// Unescaped, "Order_" would also match "OrderX-1", "Order%-1" and "Order\-1".
	assert.Equal(t, []string{"Order_-1"}, list("Order_"))
	// A trailing backslash must not swallow the appended wildcard.
	assert.Equal(t, []string{`Order\-1`}, list(`Order\`))
	// Plain prefixes keep their prefix semantics.
	assert.ElementsMatch(t, []string{"Order%-1", "OrderX-1", "Order_-1", `Order\-1`, "Order-1"}, list("Order"))
	assert.Equal(t, []string{"Order-1"}, list("Order-"))
}

// ---------------------------------------------------------------------------
// StreamsBySubject: tolerance of a malformed $subjects tag
// ---------------------------------------------------------------------------

func TestAppearsVerbatimInJSON(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"u1", true},
		{"a b-c_d.e@f:g", true},
		{`u"1`, false},
		{`a\b`, false},
		{"<x>", false},
		{"a&b", false},
		{"it's", false},
		{"a/b", false},
		{"ü", false},
		{"a\x01", false},
		{"a\x7f", false},
		{"a\tb", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.in), func(t *testing.T) {
			assert.Equal(t, tt.want, appearsVerbatimInJSON(tt.in))
		})
	}
}

type fakeSQLStateError struct{ code string }

func (e fakeSQLStateError) Error() string    { return "fake " + e.code }
func (e fakeSQLStateError) SQLState() string { return e.code }

func TestSqlState(t *testing.T) {
	assert.Equal(t, "", sqlState(nil))
	assert.Equal(t, "", sqlState(errors.New("plain")))
	assert.Equal(t, "22P02", sqlState(fakeSQLStateError{"22P02"}))
	assert.Equal(t, "22P02", sqlState(fmt.Errorf("wrapped: %w", fakeSQLStateError{"22P02"})))
}

func TestPostgresAdapter_StreamsBySubject_ToleratesMalformedTagOnUnrelatedRow(t *testing.T) {
	adapter := setupIntegrationTest(t)
	ctx := context.Background()

	tagger := func(_ string, _ []byte, md mink.Metadata) []string {
		if md.UserID != "" {
			return []string{md.UserID}
		}
		return nil
	}
	store := mink.New(adapter, mink.WithSubjectTagger(tagger))
	store.RegisterEvents(indexTestEvent{})
	appendFor := func(streamID, userID string) {
		t.Helper()
		require.NoError(t, store.Append(ctx, streamID, []interface{}{indexTestEvent{UserID: userID}}, mink.WithAppendMetadata(mink.Metadata{UserID: userID})))
	}
	appendFor("User-u1", "u1")
	appendFor("Order-o1", "u1")
	appendFor("User-u2", "u2")
	appendFor("Legacy-1", "u3")
	appendFor("Quote-1", `ü"1`) // a subject id JSON encoders escape: forces the unprefiltered scan

	healthy, err := adapter.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, healthy)

	// Poison the tag on a row that belongs to a different subject entirely.
	_, err = adapter.DB().ExecContext(ctx,
		`UPDATE `+adapter.schemaQ+`.events SET metadata = jsonb_set(metadata, '{custom,$subjects}', to_jsonb('{not json'::text)) WHERE stream_id = $1`,
		"Legacy-1")
	require.NoError(t, err)

	// The JSONB fast path is now broken for EVERY subject by that one row ...
	_, err = adapter.streamsBySubjectJSONB(ctx, "u1")
	require.Error(t, err)
	assert.Equal(t, pgInvalidTextRepresentation, sqlState(err))

	// ... but resolution still returns exactly what it did while the data was healthy.
	got, err := adapter.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, healthy, got)

	got, err = adapter.StreamsBySubject(ctx, "u2")
	require.NoError(t, err)
	assert.Equal(t, []string{"User-u2"}, got)

	got, err = adapter.StreamsBySubject(ctx, `ü"1`)
	require.NoError(t, err)
	assert.Equal(t, []string{"Quote-1"}, got)

	// The poisoned row cannot name any subject, so it is skipped rather than fatal.
	got, err = adapter.StreamsBySubject(ctx, "u3")
	require.NoError(t, err)
	assert.Empty(t, got)

	got, err = adapter.StreamsBySubject(ctx, "nobody")
	require.NoError(t, err)
	assert.Empty(t, got)

	// Errors other than the cast failure are still surfaced, not swallowed.
	_, err = adapter.StreamsBySubject(pgCanceledContext(), "u1")
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// ListStreams: the prefix escaping must not depend on standard_conforming_strings
// ---------------------------------------------------------------------------

func TestPostgresAdapter_ListStreams_PrefixEscapingIndependentOfStandardConformingStrings(t *testing.T) {
	connStr := requireIntegration(t)
	db, err := sql.Open("pgx", connStr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	// One pooled connection, so the session setting below governs every statement
	// the adapter issues.
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	_, err = db.ExecContext(ctx, "SET standard_conforming_strings = off")
	require.NoError(t, err)
	var setting string
	require.NoError(t, db.QueryRowContext(ctx, "SHOW standard_conforming_strings").Scan(&setting))
	require.Equal(t, "off", setting)

	schema := newTestSchema()
	t.Cleanup(func() { cleanupSchema(t, db, schema) })
	adapter := newTestAdapter(t, db, WithSchema(schema))
	require.NoError(t, adapter.Initialize(ctx))

	for _, id := range []string{"Order%-1", "OrderX-1", "Order_-1", `Order\-1`, "Order-1"} {
		_, err := adapter.Append(ctx, id, []adapters.EventRecord{{Type: "E", Data: []byte(`{}`)}}, mink.NoStream)
		require.NoError(t, err)
	}
	list := func(prefix string) []string {
		t.Helper()
		summaries, err := adapter.ListStreams(ctx, prefix, 100)
		require.NoError(t, err)
		ids := make([]string, 0, len(summaries))
		for _, s := range summaries {
			ids = append(ids, s.StreamID)
		}
		return ids
	}

	// With a literal ESCAPE '\' clause this statement would not even parse under
	// standard_conforming_strings=off; with the default escape character carried
	// only inside the bind parameter, '%' and '_' stay literal under either setting.
	assert.Equal(t, []string{"Order%-1"}, list("Order%"))
	assert.Equal(t, []string{"Order_-1"}, list("Order_"))
	assert.Equal(t, []string{`Order\-1`}, list(`Order\`))
	assert.Equal(t, []string{"Order-1"}, list("Order-"))
	assert.Len(t, list("Order"), 5)
}

// ---------------------------------------------------------------------------
// StreamsBySubject: a malformed tag that may name the subject is fatal, not silent
// ---------------------------------------------------------------------------

func TestSubjectTagMalformedError(t *testing.T) {
	err := &SubjectTagMalformedError{SubjectID: "u1", Rows: 2, Streams: 1}
	assert.ErrorIs(t, err, ErrSubjectTagMalformed)
	assert.Equal(t, ErrSubjectTagMalformed, errors.Unwrap(err))

	var typed *SubjectTagMalformedError
	require.ErrorAs(t, fmt.Errorf("wrapped: %w", err), &typed)
	assert.Equal(t, "u1", typed.SubjectID)
	assert.Equal(t, 2, typed.Rows)
	assert.Equal(t, 1, typed.Streams)

	msg := err.Error()
	assert.Contains(t, msg, `subject "u1"`)
	assert.Contains(t, msg, "2 malformed")
	assert.Contains(t, msg, "1 unresolved stream")
	assert.True(t, strings.HasPrefix(msg, "mink/postgres: "))

	assert.False(t, errors.Is(err, errors.New("other")))
	assert.False(t, errors.Is(errors.New("other"), ErrSubjectTagMalformed))
}

func TestTagMayName(t *testing.T) {
	quoted, err := json.Marshal(`a"b`)
	require.NoError(t, err)
	ampersand, err := json.Marshal("a&b")
	require.NoError(t, err)
	truncate := func(enc []byte) string { return "[" + string(enc[:len(enc)-1]) }

	tests := []struct {
		name string
		raw  string
		id   string
		want bool
	}{
		{"verbatim substring", `["u1"`, "u1", true},
		{"absent", `{not json`, "u1", false},
		{"other subject only", `["u2"]x`, "u1", false},
		{"json-escaped spelling of a quoted id", truncate(quoted), `a"b`, true},
		{"json-escaped spelling of an html-sensitive id", truncate(ampersand), "a&b", true},
		{"escaped spelling absent", `["ab"`, `a"b`, false},
		{"empty id is contained by anything", `{`, "", true},
		{"id as a prefix of a longer id still counts (conservative)", `["u10"]x`, "u1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tagMayName(tt.raw, tt.id))
		})
	}
}

func TestPostgresAdapter_StreamsBySubject_MalformedTagNamingSubjectIsFatal(t *testing.T) {
	adapter := setupIntegrationTest(t)
	ctx := context.Background()

	tagger := func(_ string, _ []byte, md mink.Metadata) []string {
		if md.UserID != "" {
			return []string{md.UserID}
		}
		return nil
	}
	store := mink.New(adapter, mink.WithSubjectTagger(tagger))
	store.RegisterEvents(indexTestEvent{})
	appendFor := func(streamID, userID string) {
		t.Helper()
		require.NoError(t, store.Append(ctx, streamID, []interface{}{indexTestEvent{UserID: userID}}, mink.WithAppendMetadata(mink.Metadata{UserID: userID})))
	}
	appendFor("User-u1", "u1")
	appendFor("Order-o1", "u1")
	appendFor("Order-o1", "u1") // a second row: the stream stays resolvable through it below
	appendFor("User-u2", "u2")
	appendFor("Quote-1", `ü"1`) // an id JSON encoders escape: forces the unprefiltered scan
	poison := func(streamID string, version int64, raw string) {
		t.Helper()
		_, err := adapter.DB().ExecContext(ctx,
			`UPDATE `+adapter.schemaQ+`.events SET metadata = jsonb_set(metadata, '{custom,$subjects}', to_jsonb($3::text)) WHERE stream_id = $1 AND version = $2`,
			streamID, version, raw)
		require.NoError(t, err)
	}

	// A truncated tag on Order-o1@2 mentions u1, but Order-o1@1 still resolves the
	// stream through a well-formed tag: nothing is lost, so the fallback succeeds.
	poison("Order-o1", 2, `["u1"`)
	_, err := adapter.streamsBySubjectJSONB(ctx, "u1")
	require.Error(t, err, "the fast path must be broken for this test to exercise the fallback")
	got, err := adapter.StreamsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"Order-o1", "User-u1"}, got)

	// Poisoning the ONLY row of User-u1 with text that mentions u1 would silently drop
	// that stream from u1's footprint: resolution must fail instead.
	poison("User-u1", 1, `{"u1":`)
	_, err = adapter.StreamsBySubject(ctx, "u1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSubjectTagMalformed)
	var typed *SubjectTagMalformedError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "u1", typed.SubjectID)
	assert.Equal(t, 1, typed.Rows, "the Order-o1@2 row is excused by Order-o1@1")
	assert.Equal(t, 1, typed.Streams)
	assert.NotContains(t, err.Error(), "User-u1", "stream ids never reach the error")
	assert.NotContains(t, err.Error(), `{"u1":`, "raw tag text never reaches the error")

	// Other subjects are unaffected: the poisoned rows do not mention them.
	got, err = adapter.StreamsBySubject(ctx, "u2")
	require.NoError(t, err)
	assert.Equal(t, []string{"User-u2"}, got)
	got, err = adapter.StreamsBySubject(ctx, "nobody")
	require.NoError(t, err)
	assert.Empty(t, got)

	// The JSON-escaped spelling of an id counts as mentioning it (unprefiltered scan).
	enc, err := json.Marshal(`ü"1`)
	require.NoError(t, err)
	poison("Quote-1", 1, "["+string(enc[:len(enc)-1])) // truncated array holding the escaped spelling
	_, err = adapter.StreamsBySubject(ctx, `ü"1`)
	assert.ErrorIs(t, err, ErrSubjectTagMalformed)
}

func TestPostgresAdapter_StreamsBySubject_SortedBytewiseOnBothPaths(t *testing.T) {
	adapter := setupIntegrationTest(t)
	ctx := context.Background()

	store := mink.New(adapter, mink.WithSubjectTagger(func(_ string, _ []byte, md mink.Metadata) []string {
		if md.UserID != "" {
			return []string{md.UserID}
		}
		return nil
	}))
	store.RegisterEvents(indexTestEvent{})
	appendFor := func(streamID, userID string) {
		t.Helper()
		require.NoError(t, store.Append(ctx, streamID, []interface{}{indexTestEvent{UserID: userID}}, mink.WithAppendMetadata(mink.Metadata{UserID: userID})))
	}
	// Collation-sensitive names: a locale-aware ORDER BY interleaves case and
	// punctuation; byte order puts "B" < "Z" < "_" < "a".
	for _, id := range []string{"a-1", "B-1", "_x-1", "Zed-1"} {
		appendFor(id, "s")
	}
	appendFor("Other-1", "o")

	want := []string{"B-1", "Zed-1", "_x-1", "a-1"}
	require.True(t, sort.StringsAreSorted(want))

	fast, err := adapter.streamsBySubjectJSONB(ctx, "s")
	require.NoError(t, err)
	assert.Equal(t, want, fast)

	scan, err := adapter.streamsBySubjectScan(ctx, "s")
	require.NoError(t, err)
	assert.Equal(t, want, scan)

	// The public method takes the fast path while healthy and the scan once an
	// unrelated row is poisoned; both must agree exactly.
	healthy, err := adapter.StreamsBySubject(ctx, "s")
	require.NoError(t, err)
	_, err = adapter.DB().ExecContext(ctx,
		`UPDATE `+adapter.schemaQ+`.events SET metadata = jsonb_set(metadata, '{custom,$subjects}', to_jsonb('{broken'::text)) WHERE stream_id = $1`,
		"Other-1")
	require.NoError(t, err)
	fallback, err := adapter.StreamsBySubject(ctx, "s")
	require.NoError(t, err)
	assert.Equal(t, healthy, fallback)
	assert.Equal(t, want, fallback)
}

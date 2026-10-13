package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev"
)

func TestSubjectTagger_DerivesSubjectFromPayloadOnly(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []string
	}{
		{"customer created", `{"customer_id":"alice-1","name":"Alice"}`, []string{"alice-1"}},
		{"order placed", `{"order_id":"ord-1","customer_id":"alice-1"}`, []string{"alice-1"}},
		{"payment received", `{"payment_id":"pay-1","customer_id":"alice-1"}`, []string{"alice-1"}},
		{"no subject field", `{"order_id":"ord-1"}`, nil},
		{"empty subject", `{"customer_id":""}`, nil},
		{"malformed json", `{not json`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Metadata is deliberately ignored: a caller-supplied UserID must never become a tag.
			got := subjectTagger("AnyEvent", []byte(tt.data), mink.Metadata{UserID: "someone-else"})
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSubjectExport_FootprintIsSubjectScopedNotActorScoped(t *testing.T) {
	ctx := context.Background()

	provider, err := newProvider()
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close() })

	store := newStore(provider)
	require.NoError(t, seedData(ctx, store))

	// Resolver-based: the complete, provably complete footprint.
	resolved := mink.NewDataExporter(store, mink.WithExportSubjectResolver(mink.NewSubjectResolver(store)))
	res, err := resolved.Export(ctx, mink.ExportRequest{SubjectID: "alice-1"})
	require.NoError(t, err)
	assert.False(t, res.Partial, "every seeded event carries a customer_id, so the footprint is complete")
	assert.Equal(t, 3, res.TotalEvents)
	assert.ElementsMatch(t, []string{"Customer-alice-1", "Order-ord-1", "Payment-pay-1"}, res.Streams)

	// SubjectFilter by scan reaches the same events.
	scan := mink.NewDataExporter(store)
	res, err = scan.Export(ctx, mink.ExportRequest{SubjectID: "alice-1", Filter: mink.SubjectFilter("alice-1")})
	require.NoError(t, err)
	assert.Equal(t, 3, res.TotalEvents)

	// FilterByUserID is actor-scoped: only the order Alice issued herself.
	res, err = scan.Export(ctx, mink.ExportRequest{SubjectID: "alice-1", Filter: mink.FilterByUserID("alice-1")})
	require.NoError(t, err)
	assert.Equal(t, 1, res.TotalEvents, "actor filter must not be mistaken for a subject footprint")

	// Another subject's footprint never includes Alice's events.
	res, err = resolved.Export(ctx, mink.ExportRequest{SubjectID: "bob-1"})
	require.NoError(t, err)
	assert.Equal(t, 2, res.TotalEvents)
	assert.ElementsMatch(t, []string{"Customer-bob-1", "Order-ord-2"}, res.Streams)
}

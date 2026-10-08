package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Outbox destination label
//
// The "destination" label is recorded exactly as given. Cardinality is bounded
// at the source: the OutboxProcessor passes the publisher prefix ("webhook"),
// never the full destination (see TestOutboxProcessor_Metrics_ReceiveOnlyPrefix
// in the root package), so the metrics layer neither rewrites nor needs an
// opt-out for the label value.
// =============================================================================

func TestMetrics_RecordMessageProcessed_LabelIsValueGiven(t *testing.T) {
	m := New(WithNamespace("outbox_processed_label_test"))
	registry := prometheus.NewRegistry()
	require.NoError(t, m.Register(registry))

	m.RecordMessageProcessed("webhook", true)
	m.RecordMessageProcessed("webhook", true)
	m.RecordMessageProcessed("webhook", false)
	m.RecordMessageProcessed("kafka", true)

	assert.Equal(t, 3, testutil.CollectAndCount(m.outboxProcessedTotal))
	assert.Equal(t, float64(2), testutil.ToFloat64(m.outboxProcessedTotal.WithLabelValues("webhook", StatusSuccess)))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.outboxProcessedTotal.WithLabelValues("webhook", StatusError)))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.outboxProcessedTotal.WithLabelValues("kafka", StatusSuccess)))
}

func TestMetrics_RecordMessageFailed_LabelIsValueGiven(t *testing.T) {
	m := New(WithNamespace("outbox_failed_label_test"))
	registry := prometheus.NewRegistry()
	require.NoError(t, m.Register(registry))

	m.RecordMessageFailed("webhook")
	m.RecordMessageFailed("webhook")
	m.RecordMessageFailed("kafka")

	assert.Equal(t, 2, testutil.CollectAndCount(m.outboxFailedTotal))
	assert.Equal(t, float64(2), testutil.ToFloat64(m.outboxFailedTotal.WithLabelValues("webhook")))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.outboxFailedTotal.WithLabelValues("kafka")))
}

func TestMetrics_OutboxLabelNames(t *testing.T) {
	m := New(WithNamespace("outbox_label_names_test"))
	registry := prometheus.NewRegistry()
	require.NoError(t, m.Register(registry))

	m.RecordMessageProcessed("webhook", true)
	m.RecordMessageFailed("webhook")

	families, err := registry.Gather()
	require.NoError(t, err)

	var sawProcessed, sawFailed bool
	for _, family := range families {
		switch family.GetName() {
		case "outbox_label_names_test_outbox_messages_processed_total":
			sawProcessed = true
			require.Len(t, family.GetMetric(), 1)
			labels := family.GetMetric()[0].GetLabel()
			require.Len(t, labels, 2)
			assert.Equal(t, LabelDestination, labels[0].GetName())
			assert.Equal(t, "webhook", labels[0].GetValue())
			assert.Equal(t, LabelStatus, labels[1].GetName())
		case "outbox_label_names_test_outbox_messages_failed_total":
			sawFailed = true
			require.Len(t, family.GetMetric(), 1)
			labels := family.GetMetric()[0].GetLabel()
			require.Len(t, labels, 1)
			assert.Equal(t, LabelDestination, labels[0].GetName())
			assert.Equal(t, "webhook", labels[0].GetValue())
		}
	}
	assert.True(t, sawProcessed, "processed counter not gathered")
	assert.True(t, sawFailed, "failed counter not gathered")
}

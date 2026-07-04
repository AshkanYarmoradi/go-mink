package containers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKafkaContainer_BrokerList(t *testing.T) {
	tests := []struct {
		name    string
		brokers string
		want    []string
	}{
		{"single", "localhost:9092", []string{"localhost:9092"}},
		{"multiple", "a:9092,b:9092", []string{"a:9092", "b:9092"}},
		{"whitespace trimmed", " a:9092 , b:9092 ", []string{"a:9092", "b:9092"}},
		{"empty entries dropped", "a:9092,,", []string{"a:9092"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := &KafkaContainer{Brokers: tt.brokers}
			assert.Equal(t, tt.want, k.BrokerList())
			assert.Equal(t, tt.want[0], k.firstBroker())
		})
	}
}

// TestStartKafka_SkipsWithoutBrokers documents the self-skip contract: with TEST_KAFKA_BROKERS
// unset, StartKafka skips the calling test rather than failing it. Run in an environment where
// the var is unset; when it is set (CI/local infra) this test is itself skipped by StartKafka.
func TestStartKafka_SkipsWithoutBrokers(t *testing.T) {
	// StartKafka calls t.Skip when TEST_KAFKA_BROKERS is unset/unreachable or under -short.
	// Reaching any assertion after it means a broker was configured and reachable.
	k := StartKafka(t)
	assert.NotEmpty(t, k.Brokers)
}

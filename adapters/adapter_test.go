package adapters

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"ErrConcurrencyConflict", ErrConcurrencyConflict},
		{"ErrStreamNotFound", ErrStreamNotFound},
		{"ErrEmptyStreamID", ErrEmptyStreamID},
		{"ErrNoEvents", ErrNoEvents},
		{"ErrInvalidVersion", ErrInvalidVersion},
		{"ErrAdapterClosed", ErrAdapterClosed},
	}

	for _, tt := range tests {
		t.Run(tt.name+" has mink prefix", func(t *testing.T) {
			assert.Contains(t, tt.err.Error(), "mink:")
		})

		t.Run(tt.name+" is distinct", func(t *testing.T) {
			for _, other := range tests {
				if tt.name != other.name {
					assert.False(t, errors.Is(tt.err, other.err),
						"%s should not match %s", tt.name, other.name)
				}
			}
		})
	}
}

func TestMetadata(t *testing.T) {
	t.Run("empty metadata", func(t *testing.T) {
		m := Metadata{}

		assert.Empty(t, m.CorrelationID)
		assert.Empty(t, m.CausationID)
		assert.Empty(t, m.UserID)
		assert.Empty(t, m.TenantID)
		assert.Nil(t, m.Custom)
	})

	t.Run("populated metadata", func(t *testing.T) {
		m := Metadata{
			CorrelationID: "corr-123",
			CausationID:   "cause-456",
			UserID:        "user-789",
			TenantID:      "tenant-abc",
			Custom:        map[string]string{"key": "value"},
		}

		assert.Equal(t, "corr-123", m.CorrelationID)
		assert.Equal(t, "cause-456", m.CausationID)
		assert.Equal(t, "user-789", m.UserID)
		assert.Equal(t, "tenant-abc", m.TenantID)
		assert.Equal(t, "value", m.Custom["key"])
	})
}

func TestStoredEvent(t *testing.T) {
	t.Run("stored event fields", func(t *testing.T) {
		now := time.Now()
		e := StoredEvent{
			ID:             "evt-123",
			StreamID:       "Order-456",
			Type:           "OrderCreated",
			Data:           []byte(`{"orderId":"456"}`),
			Metadata:       Metadata{UserID: "user-789"},
			Version:        1,
			GlobalPosition: 100,
			Timestamp:      now,
		}

		assert.Equal(t, "evt-123", e.ID)
		assert.Equal(t, "Order-456", e.StreamID)
		assert.Equal(t, "OrderCreated", e.Type)
		assert.Equal(t, []byte(`{"orderId":"456"}`), e.Data)
		assert.Equal(t, "user-789", e.Metadata.UserID)
		assert.Equal(t, int64(1), e.Version)
		assert.Equal(t, uint64(100), e.GlobalPosition)
		assert.Equal(t, now, e.Timestamp)
	})
}

func TestStreamInfo(t *testing.T) {
	t.Run("stream info fields", func(t *testing.T) {
		now := time.Now()
		info := StreamInfo{
			StreamID:   "Order-123",
			Category:   "Order",
			Version:    5,
			EventCount: 10,
			CreatedAt:  now.Add(-time.Hour),
			UpdatedAt:  now,
		}

		assert.Equal(t, "Order-123", info.StreamID)
		assert.Equal(t, "Order", info.Category)
		assert.Equal(t, int64(5), info.Version)
		assert.Equal(t, int64(10), info.EventCount)
		assert.True(t, info.CreatedAt.Before(info.UpdatedAt))
	})
}

func TestEventRecord(t *testing.T) {
	t.Run("event record fields", func(t *testing.T) {
		r := EventRecord{
			Type:     "OrderCreated",
			Data:     []byte(`{"orderId":"123"}`),
			Metadata: Metadata{CorrelationID: "corr-456"},
		}

		assert.Equal(t, "OrderCreated", r.Type)
		assert.Equal(t, []byte(`{"orderId":"123"}`), r.Data)
		assert.Equal(t, "corr-456", r.Metadata.CorrelationID)
	})
}

func TestSnapshotRecord(t *testing.T) {
	t.Run("snapshot record fields", func(t *testing.T) {
		s := SnapshotRecord{
			StreamID: "Order-123",
			Version:  10,
			Data:     []byte(`{"state":"active"}`),
		}

		assert.Equal(t, "Order-123", s.StreamID)
		assert.Equal(t, int64(10), s.Version)
		assert.Equal(t, []byte(`{"state":"active"}`), s.Data)
	})
}

func TestErrorMessages(t *testing.T) {
	t.Run("ErrConcurrencyConflict message", func(t *testing.T) {
		assert.Equal(t, "mink: concurrency conflict", ErrConcurrencyConflict.Error())
	})

	t.Run("ErrStreamNotFound message", func(t *testing.T) {
		assert.Equal(t, "mink: stream not found", ErrStreamNotFound.Error())
	})

	t.Run("ErrEmptyStreamID message", func(t *testing.T) {
		assert.Equal(t, "mink: stream ID is required", ErrEmptyStreamID.Error())
	})

	t.Run("ErrNoEvents message", func(t *testing.T) {
		assert.Equal(t, "mink: no events to append", ErrNoEvents.Error())
	})

	t.Run("ErrInvalidVersion message", func(t *testing.T) {
		assert.Equal(t, "mink: invalid version", ErrInvalidVersion.Error())
	})

	t.Run("ErrAdapterClosed message", func(t *testing.T) {
		assert.Equal(t, "mink: adapter is closed", ErrAdapterClosed.Error())
	})
}

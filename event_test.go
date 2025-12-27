package mink

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionConstants(t *testing.T) {
	t.Run("AnyVersion is negative", func(t *testing.T) {
		assert.Equal(t, int64(-1), AnyVersion)
	})

	t.Run("NoStream is zero", func(t *testing.T) {
		assert.Equal(t, int64(0), NoStream)
	})

	t.Run("StreamExists is negative", func(t *testing.T) {
		assert.Equal(t, int64(-2), StreamExists)
	})

	t.Run("constants are distinct", func(t *testing.T) {
		assert.NotEqual(t, AnyVersion, NoStream)
		assert.NotEqual(t, AnyVersion, StreamExists)
		assert.NotEqual(t, NoStream, StreamExists)
	})
}

func TestStreamID(t *testing.T) {
	t.Run("NewStreamID creates valid ID", func(t *testing.T) {
		sid := NewStreamID("Order", "123")

		assert.Equal(t, "Order", sid.Category)
		assert.Equal(t, "123", sid.ID)
	})

	t.Run("String formats correctly", func(t *testing.T) {
		sid := NewStreamID("Order", "123")

		assert.Equal(t, "Order-123", sid.String())
	})

	t.Run("ParseStreamID parses valid format", func(t *testing.T) {
		sid, err := ParseStreamID("Order-123")

		require.NoError(t, err)
		assert.Equal(t, "Order", sid.Category)
		assert.Equal(t, "123", sid.ID)
	})

	t.Run("ParseStreamID handles ID with hyphens", func(t *testing.T) {
		sid, err := ParseStreamID("Order-123-456-789")

		require.NoError(t, err)
		assert.Equal(t, "Order", sid.Category)
		assert.Equal(t, "123-456-789", sid.ID)
	})

	t.Run("ParseStreamID returns error for invalid format", func(t *testing.T) {
		tests := []struct {
			name  string
			input string
		}{
			{"empty string", ""},
			{"no hyphen", "Order123"},
			{"empty category", "-123"},
			{"empty ID", "Order-"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := ParseStreamID(tt.input)
				assert.Error(t, err)
			})
		}
	})

	t.Run("IsZero detects empty StreamID", func(t *testing.T) {
		empty := StreamID{}
		assert.True(t, empty.IsZero())

		partial1 := StreamID{Category: "Order"}
		assert.False(t, partial1.IsZero())

		partial2 := StreamID{ID: "123"}
		assert.False(t, partial2.IsZero())

		full := NewStreamID("Order", "123")
		assert.False(t, full.IsZero())
	})

	t.Run("Validate checks required fields", func(t *testing.T) {
		tests := []struct {
			name    string
			sid     StreamID
			wantErr bool
		}{
			{"valid", NewStreamID("Order", "123"), false},
			{"empty category", StreamID{ID: "123"}, true},
			{"empty ID", StreamID{Category: "Order"}, true},
			{"both empty", StreamID{}, true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := tt.sid.Validate()
				if tt.wantErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			})
		}
	})
}

func TestMetadata(t *testing.T) {
	t.Run("empty metadata", func(t *testing.T) {
		m := Metadata{}
		assert.True(t, m.IsEmpty())
	})

	t.Run("WithCorrelationID", func(t *testing.T) {
		m := Metadata{}.WithCorrelationID("corr-123")

		assert.Equal(t, "corr-123", m.CorrelationID)
		assert.False(t, m.IsEmpty())
	})

	t.Run("WithCausationID", func(t *testing.T) {
		m := Metadata{}.WithCausationID("cause-456")

		assert.Equal(t, "cause-456", m.CausationID)
		assert.False(t, m.IsEmpty())
	})

	t.Run("WithUserID", func(t *testing.T) {
		m := Metadata{}.WithUserID("user-789")

		assert.Equal(t, "user-789", m.UserID)
		assert.False(t, m.IsEmpty())
	})

	t.Run("WithTenantID", func(t *testing.T) {
		m := Metadata{}.WithTenantID("tenant-abc")

		assert.Equal(t, "tenant-abc", m.TenantID)
		assert.False(t, m.IsEmpty())
	})

	t.Run("WithCustom adds key-value", func(t *testing.T) {
		m := Metadata{}.WithCustom("key1", "value1")

		assert.Equal(t, "value1", m.Custom["key1"])
		assert.False(t, m.IsEmpty())
	})

	t.Run("WithCustom preserves existing values", func(t *testing.T) {
		m := Metadata{}.
			WithCustom("key1", "value1").
			WithCustom("key2", "value2")

		assert.Equal(t, "value1", m.Custom["key1"])
		assert.Equal(t, "value2", m.Custom["key2"])
	})

	t.Run("chained methods", func(t *testing.T) {
		m := Metadata{}.
			WithCorrelationID("corr-123").
			WithCausationID("cause-456").
			WithUserID("user-789").
			WithTenantID("tenant-abc").
			WithCustom("env", "production")

		assert.Equal(t, "corr-123", m.CorrelationID)
		assert.Equal(t, "cause-456", m.CausationID)
		assert.Equal(t, "user-789", m.UserID)
		assert.Equal(t, "tenant-abc", m.TenantID)
		assert.Equal(t, "production", m.Custom["env"])
		assert.False(t, m.IsEmpty())
	})

	t.Run("IsEmpty with only custom", func(t *testing.T) {
		m := Metadata{Custom: map[string]string{"key": "value"}}
		assert.False(t, m.IsEmpty())
	})
}

func TestEventData(t *testing.T) {
	t.Run("NewEventData creates valid event", func(t *testing.T) {
		data := []byte(`{"orderId":"123"}`)
		e := NewEventData("OrderCreated", data)

		assert.Equal(t, "OrderCreated", e.Type)
		assert.Equal(t, data, e.Data)
		assert.True(t, e.Metadata.IsEmpty())
	})

	t.Run("WithMetadata sets metadata", func(t *testing.T) {
		e := NewEventData("OrderCreated", []byte(`{}`)).
			WithMetadata(Metadata{}.WithUserID("user-123"))

		assert.Equal(t, "user-123", e.Metadata.UserID)
	})

	t.Run("Validate checks required fields", func(t *testing.T) {
		tests := []struct {
			name    string
			event   EventData
			wantErr bool
		}{
			{
				name:    "valid event",
				event:   NewEventData("OrderCreated", []byte(`{}`)),
				wantErr: false,
			},
			{
				name:    "empty type",
				event:   EventData{Data: []byte(`{}`)},
				wantErr: true,
			},
			{
				name:    "nil data",
				event:   EventData{Type: "OrderCreated"},
				wantErr: true,
			},
			{
				name:    "empty data",
				event:   EventData{Type: "OrderCreated", Data: []byte{}},
				wantErr: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := tt.event.Validate()
				if tt.wantErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			})
		}
	})
}

func TestStoredEvent(t *testing.T) {
	t.Run("contains all fields", func(t *testing.T) {
		now := time.Now()
		e := StoredEvent{
			ID:             "evt-123",
			StreamID:       "Order-456",
			Type:           "OrderCreated",
			Data:           []byte(`{"orderId":"456"}`),
			Metadata:       Metadata{}.WithUserID("user-789"),
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
	t.Run("contains all fields", func(t *testing.T) {
		now := time.Now()
		info := StreamInfo{
			StreamID:  "Order-123",
			Category:  "Order",
			Version:   5,
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now,
		}

		assert.Equal(t, "Order-123", info.StreamID)
		assert.Equal(t, "Order", info.Category)
		assert.Equal(t, int64(5), info.Version)
		assert.Equal(t, now.Add(-time.Hour), info.CreatedAt)
		assert.Equal(t, now, info.UpdatedAt)
	})
}

func TestEvent(t *testing.T) {
	t.Run("EventFromStored converts correctly", func(t *testing.T) {
		now := time.Now()
		stored := StoredEvent{
			ID:             "evt-123",
			StreamID:       "Order-456",
			Type:           "OrderCreated",
			Data:           []byte(`{"orderId":"456"}`),
			Metadata:       Metadata{}.WithUserID("user-789"),
			Version:        1,
			GlobalPosition: 100,
			Timestamp:      now,
		}

		type OrderCreated struct {
			OrderID string `json:"orderId"`
		}
		data := OrderCreated{OrderID: "456"}

		event := EventFromStored(stored, data)

		assert.Equal(t, "evt-123", event.ID)
		assert.Equal(t, "Order-456", event.StreamID)
		assert.Equal(t, "OrderCreated", event.Type)
		assert.Equal(t, data, event.Data)
		assert.Equal(t, "user-789", event.Metadata.UserID)
		assert.Equal(t, int64(1), event.Version)
		assert.Equal(t, uint64(100), event.GlobalPosition)
		assert.Equal(t, now, event.Timestamp)
	})
}

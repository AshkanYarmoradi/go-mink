package mink

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersion(t *testing.T) {
	v := Version()

	assert.Equal(t, "0.2.0", v)
}

func TestBuildStreamID(t *testing.T) {
	tests := []struct {
		name          string
		aggregateType string
		aggregateID   string
		want          string
	}{
		{
			name:          "standard stream ID",
			aggregateType: "Order",
			aggregateID:   "123",
			want:          "Order-123",
		},
		{
			name:          "UUID ID",
			aggregateType: "Customer",
			aggregateID:   "550e8400-e29b-41d4-a716-446655440000",
			want:          "Customer-550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:          "empty type",
			aggregateType: "",
			aggregateID:   "123",
			want:          "-123",
		},
		{
			name:          "empty ID",
			aggregateType: "Order",
			aggregateID:   "",
			want:          "Order-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildStreamID(tt.aggregateType, tt.aggregateID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseStreamID(t *testing.T) {
	tests := []struct {
		name         string
		streamID     string
		wantCategory string
		wantID       string
		wantErr      bool
	}{
		{
			name:         "standard stream ID",
			streamID:     "Order-123",
			wantCategory: "Order",
			wantID:       "123",
			wantErr:      false,
		},
		{
			name:         "UUID ID",
			streamID:     "Customer-550e8400-e29b-41d4-a716-446655440000",
			wantCategory: "Customer",
			wantID:       "550e8400-e29b-41d4-a716-446655440000",
			wantErr:      false,
		},
		{
			name:     "no dash",
			streamID: "Order123",
			wantErr:  true,
		},
		{
			name:     "empty string",
			streamID: "",
			wantErr:  true,
		},
		{
			name:     "just dash",
			streamID: "-",
			wantErr:  true,
		},
		{
			name:         "multiple dashes - takes first",
			streamID:     "Order-123-456",
			wantCategory: "Order",
			wantID:       "123-456",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStreamID(tt.streamID)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantCategory, got.Category)
			assert.Equal(t, tt.wantID, got.ID)
		})
	}
}

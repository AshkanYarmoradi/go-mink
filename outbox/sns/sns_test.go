package sns

import (
	"context"
	"testing"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSNSClient implements SNSClient for testing.
type mockSNSClient struct {
	publishCalls []*sns.PublishInput
	publishErr   error
}

func (m *mockSNSClient) Publish(ctx context.Context, params *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error) {
	m.publishCalls = append(m.publishCalls, params)
	if m.publishErr != nil {
		return nil, m.publishErr
	}
	return &sns.PublishOutput{
		MessageId: stringPtr("msg-123"),
	}, nil
}

func TestPublisher_Destination(t *testing.T) {
	p := New()
	assert.Equal(t, "sns", p.Destination())
}

func TestPublisher_Publish_Success(t *testing.T) {
	mock := &mockSNSClient{}
	p := New(WithSNSClient(mock))
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{
			ID:          "msg-1",
			Destination: "sns:arn:aws:sns:us-east-1:123456789:my-topic",
			Payload:     []byte(`{"event":"OrderCreated"}`),
			Headers: map[string]string{
				"event-type": "OrderCreated",
			},
		},
	}

	err := p.Publish(ctx, messages)
	require.NoError(t, err)
	require.Len(t, mock.publishCalls, 1)

	call := mock.publishCalls[0]
	assert.Equal(t, "arn:aws:sns:us-east-1:123456789:my-topic", *call.TopicArn)
	assert.Equal(t, `{"event":"OrderCreated"}`, *call.Message)
	assert.Contains(t, call.MessageAttributes, "event-type")
}

func TestPublisher_Publish_WithMessageGroupID(t *testing.T) {
	mock := &mockSNSClient{}
	p := New(WithSNSClient(mock), WithMessageGroupID("orders"))
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{
			ID:          "msg-1",
			Destination: "sns:arn:aws:sns:us-east-1:123456789:my-topic.fifo",
			Payload:     []byte(`{}`),
		},
	}

	err := p.Publish(ctx, messages)
	require.NoError(t, err)
	assert.Equal(t, "orders", *mock.publishCalls[0].MessageGroupId)
}

func TestPublisher_Publish_NoClient(t *testing.T) {
	p := New()
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: "sns:arn", Payload: []byte(`{}`)},
	}

	err := p.Publish(ctx, messages)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "client not configured")
}

func TestPublisher_Publish_InvalidDestination(t *testing.T) {
	mock := &mockSNSClient{}
	p := New(WithSNSClient(mock))
	ctx := context.Background()

	messages := []*adapters.OutboxMessage{
		{ID: "msg-1", Destination: "invalid", Payload: []byte(`{}`)},
	}

	err := p.Publish(ctx, messages)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing topic ARN")
}

func TestExtractTopicARN(t *testing.T) {
	tests := []struct {
		destination string
		want        string
	}{
		{"sns:arn:aws:sns:us-east-1:123456789:my-topic", "arn:aws:sns:us-east-1:123456789:my-topic"},
		{"kafka:topic", ""},
		{"sns:", ""},
	}

	for _, tt := range tests {
		t.Run(tt.destination, func(t *testing.T) {
			got := extractTopicARN(tt.destination)
			assert.Equal(t, tt.want, got)
		})
	}
}

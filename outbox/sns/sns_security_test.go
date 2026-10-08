package sns

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
)

const testTopic = "sns:arn:aws:sns:us-east-1:123456789012:orders"

func TestPublisher_Publish_RejectsReservedAttributes(t *testing.T) {
	reserved := []string{
		"AWS.SNS.SMS.SenderID",
		"AWS.SNS.SMS.MaxPrice",
		"AWS.SNS.SMS.SMSType",
		"AWS.SNS.MOBILE.APNS.PUSH_TYPE",
		"aws.sns.sms.maxprice", // case-insensitive
		"Amazon.Anything",
		"AMAZON.x",
	}
	for _, name := range reserved {
		t.Run(name, func(t *testing.T) {
			mock := &mockSNSClient{}
			p := New(WithSNSClient(mock))
			msgs := []*adapters.OutboxMessage{{
				ID: "msg-1", Destination: testTopic, Payload: []byte(`{}`),
				Headers: map[string]string{"event-type": "OrderCreated", name: "attacker"},
			}}

			err := p.Publish(context.Background(), msgs)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrReservedAttribute)
			assert.Contains(t, err.Error(), name)
			assert.Contains(t, err.Error(), "msg-1")
			assert.Empty(t, mock.publishCalls, "a message with a reserved attribute must not reach SNS")
		})
	}
}

func TestPublisher_Publish_WithAllowReservedAttributes(t *testing.T) {
	mock := &mockSNSClient{}
	p := New(WithSNSClient(mock), WithAllowReservedAttributes())
	msgs := []*adapters.OutboxMessage{{
		ID: "msg-1", Destination: testTopic, Payload: []byte(`{}`),
		Headers: map[string]string{"AWS.SNS.SMS.SenderID": "MyBrand"},
	}}

	require.NoError(t, p.Publish(context.Background(), msgs))
	require.Len(t, mock.publishCalls, 1)
	attr, ok := mock.publishCalls[0].MessageAttributes["AWS.SNS.SMS.SenderID"]
	require.True(t, ok)
	assert.Equal(t, "MyBrand", *attr.StringValue)
	assert.Equal(t, "String", *attr.DataType)
}

func TestPublisher_Publish_RejectsInvalidAttributeNames(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		substr string
	}{
		{"empty", "", "empty name"},
		{"space", "event type", "outside"},
		{"colon", "event:type", "outside"},
		{"crlf", "x\r\ny", "outside"},
		{"non-ascii", "évent", "outside"},
		{"leading period", ".event", "period"},
		{"trailing period", "event.", "period"},
		{"double period", "ev..ent", "period"},
		{"too long", strings.Repeat("a", maxAttributeNameLen+1), "longer than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockSNSClient{}
			p := New(WithSNSClient(mock))
			msgs := []*adapters.OutboxMessage{{
				ID: "msg-1", Destination: testTopic, Payload: []byte(`{}`),
				Headers: map[string]string{tt.key: "v"},
			}}

			err := p.Publish(context.Background(), msgs)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidAttributeName)
			assert.Contains(t, err.Error(), tt.substr)
			assert.Empty(t, mock.publishCalls)
		})
	}
}

func TestValidateAttributeName_AcceptsValidNames(t *testing.T) {
	valid := []string{
		"event-type", "stream-id", "correlation_id", "causation.id", "EventType1", "a",
		"a.b-c_d", strings.Repeat("a", maxAttributeNameLen),
	}
	for _, name := range valid {
		assert.NoError(t, validateAttributeName(name), name)
	}
}

func TestPublisher_Publish_InvalidAttributeDoesNotBlockOtherMessages(t *testing.T) {
	mock := &mockSNSClient{}
	p := New(WithSNSClient(mock))
	msgs := []*adapters.OutboxMessage{
		{ID: "bad", Destination: testTopic, Payload: []byte(`{}`), Headers: map[string]string{"AWS.SNS.SMS.MaxPrice": "99"}},
		{ID: "good", Destination: testTopic, Payload: []byte(`{}`), Headers: map[string]string{"event-type": "X"}},
	}

	err := p.Publish(context.Background(), msgs)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReservedAttribute)
	require.Len(t, mock.publishCalls, 1)
	assert.Equal(t, "X", *mock.publishCalls[0].MessageAttributes["event-type"].StringValue)
}

func TestIsReservedAttribute(t *testing.T) {
	assert.True(t, isReservedAttribute("AWS.SNS.SMS.SenderID"))
	assert.True(t, isReservedAttribute("aws.x"))
	assert.True(t, isReservedAttribute("Amazon.x"))
	assert.False(t, isReservedAttribute("AWSx"))
	assert.False(t, isReservedAttribute("my.aws.key"))
	assert.False(t, isReservedAttribute("event-type"))
}

func TestPublisher_Publish_ClientError_WrapsCauseWithoutPayload(t *testing.T) {
	cause := errors.New("AccessDenied: not authorized")
	mock := &mockSNSClient{publishErr: cause}
	p := New(WithSNSClient(mock))
	msgs := []*adapters.OutboxMessage{
		{ID: "a", Destination: testTopic, Payload: []byte(`{"ssn":"123-45-6789"}`)},
		{ID: "b", Destination: testTopic, Payload: []byte(`{"ssn":"987-65-4321"}`)},
	}

	err := p.Publish(context.Background(), msgs)
	require.Error(t, err)
	assert.ErrorIs(t, err, cause)
	// Every message is still attempted; the error names the topic but never
	// echoes the payload, since the processor persists it as last_error.
	assert.Len(t, mock.publishCalls, 2)
	assert.Contains(t, err.Error(), "arn:aws:sns:us-east-1:123456789012:orders")
	assert.NotContains(t, err.Error(), "ssn")
	assert.NotContains(t, err.Error(), "123-45-6789")
}

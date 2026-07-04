package mink_test

import (
	"context"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssns "github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/stretchr/testify/require"

	"go-mink.dev/adapters"
	minksns "go-mink.dev/outbox/sns"
)

// TestE2E_SNS_Publish: the SNS outbox publisher publishes a message to a real SNS API (a goaws /
// LocalStack emulator or real AWS). Gated on TEST_SNS_ENDPOINT (self-skips otherwise).
func TestE2E_SNS_Publish(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SNS E2E in short mode")
	}
	endpoint := os.Getenv("TEST_SNS_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_SNS_ENDPOINT not set; skipping SNS E2E")
	}
	ctx := context.Background()

	client := awssns.NewFromConfig(awsTestConfig(), func(o *awssns.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
	topic, err := client.CreateTopic(ctx, &awssns.CreateTopicInput{Name: aws.String("mink-e2e")})
	require.NoError(t, err)
	arn := aws.ToString(topic.TopicArn)

	// Publish through the mink SNS publisher (destination carries the "sns:" prefix).
	pub := minksns.New(minksns.WithSNSClient(client))
	err = pub.Publish(ctx, []*adapters.OutboxMessage{{
		ID:          "m1",
		Destination: "sns:" + arn,
		Payload:     []byte(`{"event":"OrderCreated"}`),
		Headers:     map[string]string{"event-type": "OrderCreated"},
	}})
	require.NoError(t, err, "the SNS publisher delivered the message to the topic")
}

// Package sns provides an AWS SNS publisher for the outbox pattern.
// It publishes outbox messages to SNS topics.
package sns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AshkanYarmoradi/go-mink/adapters"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
)

// SNSClient defines the subset of the SNS API used by the publisher.
type SNSClient interface {
	Publish(ctx context.Context, params *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// Publisher publishes outbox messages to AWS SNS topics.
// Destination format: "sns:arn:aws:sns:region:account:topic"
type Publisher struct {
	client         SNSClient
	messageGroupID string
}

// Option configures an SNS Publisher.
type Option func(*Publisher)

// WithSNSClient sets a custom SNS client.
func WithSNSClient(client SNSClient) Option {
	return func(p *Publisher) {
		p.client = client
	}
}

// WithMessageGroupID sets the message group ID for FIFO topics.
func WithMessageGroupID(groupID string) Option {
	return func(p *Publisher) {
		p.messageGroupID = groupID
	}
}

// New creates a new SNS Publisher.
func New(opts ...Option) *Publisher {
	p := &Publisher{}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Destination returns the destination prefix this publisher handles.
func (p *Publisher) Destination() string {
	return "sns"
}

// Publish sends outbox messages to the SNS topic specified in the destination.
// All messages are attempted even if some fail; errors are collected and returned as a joined error.
func (p *Publisher) Publish(ctx context.Context, messages []*adapters.OutboxMessage) error {
	if p.client == nil {
		return fmt.Errorf("sns: client not configured")
	}

	var errs []error
	for _, msg := range messages {
		topicARN := extractTopicARN(msg.Destination)
		if topicARN == "" {
			errs = append(errs, fmt.Errorf("sns: invalid destination %q: missing topic ARN", msg.Destination))
			continue
		}

		input := &sns.PublishInput{
			TopicArn: &topicARN,
			Message:  stringPtr(string(msg.Payload)),
		}

		// Add message attributes from headers
		if len(msg.Headers) > 0 {
			input.MessageAttributes = make(map[string]types.MessageAttributeValue)
			for k, v := range msg.Headers {
				input.MessageAttributes[k] = types.MessageAttributeValue{
					DataType:    stringPtr("String"),
					StringValue: stringPtr(v),
				}
			}
		}

		// Set message group ID for FIFO topics
		if p.messageGroupID != "" {
			input.MessageGroupId = &p.messageGroupID
		}

		if _, err := p.client.Publish(ctx, input); err != nil {
			errs = append(errs, fmt.Errorf("sns: failed to publish to %s: %w", topicARN, err))
		}
	}

	return errors.Join(errs...)
}

// extractTopicARN removes the "sns:" prefix from a destination.
func extractTopicARN(destination string) string {
	const prefix = "sns:"
	if strings.HasPrefix(destination, prefix) {
		return destination[len(prefix):]
	}
	return ""
}

func stringPtr(s string) *string {
	return &s
}

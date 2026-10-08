// Package sns provides an AWS SNS publisher for the outbox pattern.
// It publishes outbox messages to SNS topics.
package sns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	"go-mink.dev/adapters"
)

// maxAttributeNameLen is the SNS limit on a message attribute name.
const maxAttributeNameLen = 256

var (
	// ErrReservedAttribute is returned when an outbox message header would be
	// published as a message attribute whose name starts with "AWS." or
	// "Amazon." (case-insensitive). SNS interprets such attributes as
	// delivery-control directives (for example AWS.SNS.SMS.SenderID or
	// AWS.SNS.SMS.MaxPrice), so a caller-controlled header must not be able to
	// set them. See WithAllowReservedAttributes.
	ErrReservedAttribute = errors.New("sns: reserved message attribute name")

	// ErrInvalidAttributeName is returned when an outbox message header key is
	// not a valid SNS message attribute name: it must be 1–256 characters of
	// [A-Za-z0-9_.-], must not start or end with a period, and must not
	// contain consecutive periods.
	ErrInvalidAttributeName = errors.New("sns: invalid message attribute name")
)

// SNSClient defines the subset of the SNS API used by the publisher.
type SNSClient interface {
	Publish(ctx context.Context, params *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// Publisher publishes outbox messages to AWS SNS topics.
// Destination format: "sns:arn:aws:sns:region:account:topic"
//
// Outbox message headers are published as String message attributes. Header
// keys are validated before publishing: an invalid name fails that message
// with ErrInvalidAttributeName, and a reserved "AWS."/"Amazon." name fails it
// with ErrReservedAttribute unless WithAllowReservedAttributes is set.
type Publisher struct {
	client                  SNSClient
	messageGroupID          string
	allowReservedAttributes bool
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

// WithAllowReservedAttributes lets outbox message headers whose name starts
// with "AWS." or "Amazon." through as message attributes. By default they are
// rejected, because SNS treats them as delivery-control directives (SMS
// sender id, SMS type, maximum SMS price, mobile-push options) and outbox
// headers are derived from caller-supplied values such as stream and
// correlation ids. Enable this only when the application deliberately sets
// such attributes and controls every header on the message.
func WithAllowReservedAttributes() Option {
	return func(p *Publisher) {
		p.allowReservedAttributes = true
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
			attrs, err := p.messageAttributes(msg.Headers)
			if err != nil {
				errs = append(errs, fmt.Errorf("sns: message %s not published: %w", msg.ID, err))
				continue
			}
			input.MessageAttributes = attrs
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

// messageAttributes converts outbox headers into SNS message attributes,
// rejecting invalid and (unless allowed) reserved attribute names so a
// caller-controlled header can never steer SNS delivery behaviour.
func (p *Publisher) messageAttributes(headers map[string]string) (map[string]types.MessageAttributeValue, error) {
	attrs := make(map[string]types.MessageAttributeValue, len(headers))
	for k, v := range headers {
		if err := validateAttributeName(k); err != nil {
			return nil, err
		}
		if !p.allowReservedAttributes && isReservedAttribute(k) {
			return nil, fmt.Errorf("%w: %q", ErrReservedAttribute, k)
		}
		attrs[k] = types.MessageAttributeValue{
			DataType:    stringPtr("String"),
			StringValue: stringPtr(v),
		}
	}
	return attrs, nil
}

// validateAttributeName enforces the SNS message attribute name rules.
func validateAttributeName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidAttributeName)
	}
	if len(name) > maxAttributeNameLen {
		return fmt.Errorf("%w: name longer than %d characters", ErrInvalidAttributeName, maxAttributeNameLen)
	}
	for i := 0; i < len(name); i++ {
		if !isAttributeNameByte(name[i]) {
			return fmt.Errorf("%w: %q contains a character outside [A-Za-z0-9_.-]", ErrInvalidAttributeName, name)
		}
	}
	if name[0] == '.' || name[len(name)-1] == '.' || strings.Contains(name, "..") {
		return fmt.Errorf("%w: %q must not start or end with a period or contain consecutive periods", ErrInvalidAttributeName, name)
	}
	return nil
}

func isAttributeNameByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '_' || c == '-' || c == '.'
}

// isReservedAttribute reports whether an attribute name uses an SNS-reserved
// prefix (case-insensitive).
func isReservedAttribute(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "aws.") || strings.HasPrefix(lower, "amazon.")
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

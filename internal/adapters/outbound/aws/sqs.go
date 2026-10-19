package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/junglegaming/backend-challenge-go/internal/application"
)

type SQS struct{ client *sqs.Client }

func NewSQS(region, endpoint, key, secret string) *SQS {
	config := awsconfig.Config{Region: region, Credentials: credentials.NewStaticCredentialsProvider(key, secret, "")}
	client := sqs.NewFromConfig(config, func(o *sqs.Options) { o.BaseEndpoint = baseEndpoint(endpoint) })
	return &SQS{client: client}
}
func baseEndpoint(endpoint string) *string {
	if endpoint == "" {
		return nil
	}
	return awsconfig.String(endpoint)
}

func (s *SQS) Check(ctx context.Context, queueURL string) error {
	if queueURL == "" {
		return fmt.Errorf("SQS queue URL is required")
	}
	_, err := s.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: awsconfig.String(queueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	return err
}
func (s *SQS) ApproximateMessages(ctx context.Context, queueURL string) (int64, error) {
	if queueURL == "" {
		return 0, fmt.Errorf("SQS queue URL is required")
	}
	out, err := s.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: awsconfig.String(queueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages}})
	if err != nil {
		return 0, err
	}
	var count int64
	_, err = fmt.Sscan(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)], &count)
	return count, err
}
func (s *SQS) Receive(ctx context.Context, queueURL string, max int32, wait, visibility int32) ([]Message, error) {
	out, err := s.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: awsconfig.String(queueURL), MaxNumberOfMessages: max, WaitTimeSeconds: wait, VisibilityTimeout: visibility})
	if err != nil {
		return nil, err
	}
	messages := make([]Message, 0, len(out.Messages))
	for _, item := range out.Messages {
		messages = append(messages, Message{ID: awsconfig.ToString(item.MessageId), Receipt: awsconfig.ToString(item.ReceiptHandle), Body: awsconfig.ToString(item.Body)})
	}
	return messages, nil
}
func (s *SQS) Delete(ctx context.Context, queueURL, receipt string) error {
	_, err := s.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: awsconfig.String(queueURL), ReceiptHandle: awsconfig.String(receipt)})
	return err
}
func (s *SQS) PublishEvent(ctx context.Context, queueURL string, event application.Event) error {
	body, err := json.Marshal(map[string]any{"eventId": event.ID, "eventType": event.Type, "aggregateId": event.AggregateID, "correlationId": event.CorrelationID, "occurredAt": event.OccurredAt.UTC(), "version": 1, "data": event.Data})
	if err != nil {
		return err
	}
	_, err = s.client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: awsconfig.String(queueURL), MessageBody: awsconfig.String(string(body)), MessageGroupId: awsconfig.String(event.AggregateID), MessageDeduplicationId: awsconfig.String(event.ID)})
	return err
}

type Message struct{ ID, Receipt, Body string }

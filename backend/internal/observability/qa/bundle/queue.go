package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const QueueSchemaVersion = "qa-bundle-queue-v1"

var jobSpecKeyPattern = regexp.MustCompile(`^qa-bundles/v1/jobs/[0-9a-f]{64}/spec\.json$`)

type JobQueue interface {
	Enqueue(context.Context, string) error
}

type JobConsumer interface {
	Receive(context.Context) (JobMessage, bool, error)
	Ack(context.Context, JobMessage) error
}

type JobMessage struct {
	MessageID     string
	ReceiptHandle string
	SpecKey       string
	ReceiveCount  int
}

type QueueEnvelope struct {
	SchemaVersion string `json:"schema_version"`
	SpecKey       string `json:"spec_key"`
}

type sqsClient interface {
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

func (q *SQSJobQueue) Receive(ctx context.Context) (JobMessage, bool, error) {
	if q == nil || q.client == nil {
		return JobMessage{}, false, errors.New("qa bundle SQS queue is unavailable")
	}
	output, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(q.queueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 20, VisibilityTimeout: 3600,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
	})
	if err != nil {
		return JobMessage{}, false, err
	}
	if len(output.Messages) == 0 {
		return JobMessage{}, false, nil
	}
	message := output.Messages[0]
	var envelope QueueEnvelope
	if err := json.Unmarshal([]byte(aws.ToString(message.Body)), &envelope); err != nil ||
		envelope.SchemaVersion != QueueSchemaVersion || !jobSpecKeyPattern.MatchString(envelope.SpecKey) {
		return JobMessage{}, false, errors.New("qa bundle queue envelope is invalid")
	}
	receiveCount, err := strconv.Atoi(message.Attributes["ApproximateReceiveCount"])
	if err != nil || receiveCount <= 0 || strings.TrimSpace(aws.ToString(message.ReceiptHandle)) == "" {
		return JobMessage{}, false, errors.New("qa bundle queue delivery metadata is invalid")
	}
	return JobMessage{
		MessageID: aws.ToString(message.MessageId), ReceiptHandle: aws.ToString(message.ReceiptHandle),
		SpecKey: envelope.SpecKey, ReceiveCount: receiveCount,
	}, true, nil
}

func (q *SQSJobQueue) Ack(ctx context.Context, message JobMessage) error {
	if q == nil || q.client == nil || strings.TrimSpace(message.ReceiptHandle) == "" {
		return errors.New("qa bundle queue receipt handle is invalid")
	}
	_, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(q.queueURL), ReceiptHandle: aws.String(message.ReceiptHandle),
	})
	return err
}

type SQSJobQueue struct {
	client   sqsClient
	queueURL string
}

func NewSQSJobQueue(client sqsClient, queueURL string) (*SQSJobQueue, error) {
	queueURL = strings.TrimSpace(queueURL)
	if client == nil || queueURL == "" {
		return nil, errors.New("qa bundle SQS client and queue URL are required")
	}
	return &SQSJobQueue{client: client, queueURL: queueURL}, nil
}

func (q *SQSJobQueue) Enqueue(ctx context.Context, specKey string) error {
	if q == nil || q.client == nil || !jobSpecKeyPattern.MatchString(specKey) {
		return errors.New("qa bundle job spec key is invalid")
	}
	body, err := json.Marshal(QueueEnvelope{SchemaVersion: QueueSchemaVersion, SpecKey: specKey})
	if err != nil {
		return err
	}
	_, err = q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(q.queueURL), MessageBody: aws.String(string(body)),
	})
	return err
}

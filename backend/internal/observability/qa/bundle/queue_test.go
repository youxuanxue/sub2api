//go:build unit

package bundle

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type fakeSQSClient struct {
	sent    *sqs.SendMessageInput
	receive *sqs.ReceiveMessageOutput
	deleted *sqs.DeleteMessageInput
}

func (f *fakeSQSClient) SendMessage(_ context.Context, input *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.sent = input
	return &sqs.SendMessageOutput{}, nil
}

func (f *fakeSQSClient) ReceiveMessage(_ context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return f.receive, nil
}

func (f *fakeSQSClient) DeleteMessage(_ context.Context, input *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.deleted = input
	return &sqs.DeleteMessageOutput{}, nil
}

func TestSQSJobQueueCarriesOnlyValidatedSpecKey(t *testing.T) {
	client := &fakeSQSClient{}
	queue, err := NewSQSJobQueue(client, "https://sqs.example/qa-bundle")
	if err != nil {
		t.Fatal(err)
	}
	key := "qa-bundles/v1/jobs/" + strings.Repeat("a", 64) + "/spec.json"
	if err := queue.Enqueue(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	var envelope QueueEnvelope
	if client.sent == nil || json.Unmarshal([]byte(*client.sent.MessageBody), &envelope) != nil || envelope.SpecKey != key {
		t.Fatalf("send=%+v envelope=%+v", client.sent, envelope)
	}
	if err := queue.Enqueue(context.Background(), "../foreign/spec.json"); err == nil {
		t.Fatal("Enqueue() accepted traversal key")
	}
}

func TestSQSJobQueueReceivesAndAcknowledgesValidatedEnvelope(t *testing.T) {
	key := "qa-bundles/v1/jobs/" + strings.Repeat("b", 64) + "/spec.json"
	body, _ := json.Marshal(QueueEnvelope{SchemaVersion: QueueSchemaVersion, SpecKey: key})
	client := &fakeSQSClient{receive: &sqs.ReceiveMessageOutput{Messages: []types.Message{{
		MessageId: aws.String("message-1"), ReceiptHandle: aws.String("receipt-1"), Body: aws.String(string(body)),
		Attributes: map[string]string{"ApproximateReceiveCount": "3"},
	}}}}
	queue, err := NewSQSJobQueue(client, "https://sqs.example/qa-bundle")
	if err != nil {
		t.Fatal(err)
	}
	message, ok, err := queue.Receive(context.Background())
	if err != nil || !ok || message.SpecKey != key || message.ReceiveCount != 3 {
		t.Fatalf("message=%+v ok=%v err=%v", message, ok, err)
	}
	if err := queue.Ack(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if client.deleted == nil || *client.deleted.ReceiptHandle != "receipt-1" {
		t.Fatalf("delete=%+v", client.deleted)
	}
}

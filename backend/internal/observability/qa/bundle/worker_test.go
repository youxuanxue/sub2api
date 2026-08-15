//go:build unit

package bundle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
)

type fakeJobConsumer struct {
	message JobMessage
	acked   bool
}

func (f *fakeJobConsumer) Receive(context.Context) (JobMessage, bool, error) {
	return f.message, true, nil
}

func (f *fakeJobConsumer) Ack(context.Context, JobMessage) error {
	f.acked = true
	return nil
}

func TestWorkerAcknowledgesOnlySuccessfulImmutableJob(t *testing.T) {
	spec := NewBundleJobSpec(7, 11, time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC))
	store := &recordingStore{}
	if err := PublishJobSpec(context.Background(), store, spec); err != nil {
		t.Fatal(err)
	}
	consumer := &fakeJobConsumer{message: JobMessage{SpecKey: spec.SpecKey, ReceiptHandle: "r1", ReceiveCount: 1}}
	worker := Worker{Consumer: consumer, OutputStore: store, Execute: func(context.Context, JobSpec, archive.ReadOnlyObjectStore, Store, ExecuteDeps) (JobReceipt, error) {
		return JobReceipt{JobID: spec.JobID}, nil
	}}
	processed, err := worker.RunOne(context.Background())
	if err != nil || !processed || !consumer.acked {
		t.Fatalf("processed=%v acked=%v err=%v", processed, consumer.acked, err)
	}
}

func TestWorkerPublishesScopedFailureAfterFinalAttemptWithoutAck(t *testing.T) {
	spec := NewBundleJobSpec(7, 11, time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC))
	store := &recordingStore{}
	if err := PublishJobSpec(context.Background(), store, spec); err != nil {
		t.Fatal(err)
	}
	consumer := &fakeJobConsumer{message: JobMessage{SpecKey: spec.SpecKey, ReceiptHandle: "r3", ReceiveCount: 3}}
	worker := Worker{Consumer: consumer, OutputStore: store, Execute: func(context.Context, JobSpec, archive.ReadOnlyObjectStore, Store, ExecuteDeps) (JobReceipt, error) {
		return JobReceipt{}, errors.New("injected raw verify failure")
	}}
	processed, err := worker.RunOne(context.Background())
	if err == nil || !processed || consumer.acked {
		t.Fatalf("processed=%v acked=%v err=%v", processed, consumer.acked, err)
	}
	if _, ok := store.objects[spec.FailureKey]; !ok {
		t.Fatalf("failure receipt %s was not published", spec.FailureKey)
	}
}

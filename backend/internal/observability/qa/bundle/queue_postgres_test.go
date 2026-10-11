//go:build unit

package bundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
)

func TestUS062_QueueCredentialRejectsUnsafeConnectionsWithoutLeakingSecrets(t *testing.T) {
	const good = "postgres://qa_user:secret-marker@queue.example/tokenkey_qa_queue?sslmode=verify-full"
	for _, tc := range []struct {
		name, dsn string
		mode      os.FileMode
		valid     bool
	}{
		{"valid", good, 0600, true},
		{"world-readable", good, 0644, false},
		{"user-database", strings.ReplaceAll(good, "tokenkey_qa_queue", "tokenkey"), 0600, false},
		{"unencrypted", strings.ReplaceAll(good, "verify-full", "disable"), 0600, false},
		{"query-overrides-database", good + "&dbname=tokenkey", 0600, false},
		{"query-overrides-host", good + "&host=localhost", 0600, false},
		{"duplicate-tls-mode", good + "&sslmode=disable", 0600, false},
		{"invalid-url", "postgres://secret-marker%/", 0600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "queue.dsn")
			if err := os.WriteFile(file, []byte(tc.dsn), tc.mode); err != nil {
				t.Fatal(err)
			}
			value, err := readQueueDSN(file)
			if tc.valid {
				if err != nil || value != tc.dsn {
					t.Fatalf("valid DSN rejected: %v", err)
				}
				return
			}
			if err == nil || value != "" || strings.Contains(err.Error(), "secret-marker") {
				t.Fatal("unsafe DSN accepted or credential leaked")
			}
		})
	}
}

func TestUS062_PostgresQueueSelectionDoesNotLoadAWSCredentials(t *testing.T) {
	t.Setenv("AWS_PROFILE", "not-a-real-qa-test-profile")
	_, err := OpenJobQueue(context.Background(), config.QaBundleConfig{QueueDriver: "postgres", QueuePostgresDSNFile: filepath.Join(t.TempDir(), "missing")})
	if err == nil || err.Error() != "open QA queue credential file" {
		t.Fatalf("unexpected factory path: %v", err)
	}
}

type drainConsumer struct {
	fakeJobConsumer
	stop           context.CancelFunc
	receiveContext context.Context
}

func (c *drainConsumer) Receive(ctx context.Context) (JobMessage, bool, error) {
	c.receiveContext = ctx
	c.stop()
	return c.message, true, nil
}

func TestUS062_WorkerDrainsClaimedJobAfterShutdownWithBoundedDeadline(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	spec := NewBundleJobSpec(7, 11, time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC))
	store := &recordingStore{}
	if err := PublishJobSpec(context.Background(), store, spec); err != nil {
		t.Fatal(err)
	}
	consumer := &drainConsumer{fakeJobConsumer: fakeJobConsumer{message: JobMessage{SpecKey: spec.SpecKey, ReceiveCount: 1, ReceiptHandle: "receipt"}}, stop: stop}
	executed := false
	worker := Worker{Consumer: consumer, OutputStore: store, Execute: func(jobCtx context.Context, _ JobSpec, _ archive.ReadOnlyObjectStore, _ Store, _ ExecuteDeps) (JobReceipt, error) {
		executed = true
		deadline, ok := jobCtx.Deadline()
		if jobCtx.Err() != nil || !ok || time.Until(deadline) > 55*time.Minute || time.Until(deadline) < 54*time.Minute {
			t.Fatal("claimed job did not get bounded drain time")
		}
		return JobReceipt{JobID: spec.JobID}, nil
	}}
	processed, err := worker.RunOneDraining(ctx)
	if err != nil || !processed || !executed || !consumer.acked || consumer.receiveContext.Err() == nil {
		t.Fatalf("drain failed: %v", err)
	}
}

package telemetryarchive

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeUploader struct {
	mu       sync.Mutex
	requests []PutRequest
	err      error
}

type blockingUploader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (u *blockingUploader) PutObject(_ context.Context, _ PutRequest) error {
	u.once.Do(func() { close(u.started) })
	<-u.release
	return nil
}

func (u *fakeUploader) PutObject(_ context.Context, request PutRequest) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.requests = append(u.requests, request)
	return u.err
}

func TestShadowDisabledHasNoBehavior(t *testing.T) {
	uploader := &fakeUploader{}
	shadow := New(Config{Enabled: false, Bucket: "archive", Prefix: "raw"}, uploader)
	require.False(t, shadow.Enqueue(DatasetUsage, map[string]any{"id": 1}))
	require.NoError(t, shadow.Stop(context.Background()))
	require.Empty(t, uploader.requests)
}

func TestShadowUploadsGzipJSONLWithChecksum(t *testing.T) {
	uploader := &fakeUploader{}
	shadow := New(Config{
		Enabled: true, Bucket: "archive", Prefix: "prod/raw-telemetry",
		QueueSize: 4, BatchSize: 2, FlushInterval: time.Hour, PutTimeout: time.Second,
	}, uploader)
	require.True(t, shadow.Enqueue(DatasetUsage, map[string]any{"id": 1}))
	require.True(t, shadow.Enqueue(DatasetUsage, map[string]any{"id": 2}))
	require.Eventually(t, func() bool { return shadow.Stats().Uploaded == 2 }, time.Second, 10*time.Millisecond)
	require.NoError(t, shadow.Stop(context.Background()))

	uploader.mu.Lock()
	require.Len(t, uploader.requests, 1)
	request := uploader.requests[0]
	uploader.mu.Unlock()
	require.Equal(t, "archive", request.Bucket)
	require.Contains(t, request.Key, "prod/raw-telemetry/usage/date=")
	require.Contains(t, request.Key, shadow.instance)
	require.Len(t, request.Metadata["sha256"], 64)
	reader, err := gzip.NewReader(strings.NewReader(string(request.Body)))
	require.NoError(t, err)
	raw, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "{\"id\":1}\n{\"id\":2}\n", string(raw))
}

func TestShadowUploadFailureDoesNotAffectEnqueue(t *testing.T) {
	uploader := &fakeUploader{err: errors.New("s3 unavailable")}
	shadow := New(Config{
		Enabled: true, Bucket: "archive", Prefix: "raw", QueueSize: 1,
		BatchSize: 1, FlushInterval: time.Hour, PutTimeout: time.Second,
	}, uploader)
	require.True(t, shadow.Enqueue(DatasetOpsError, map[string]any{"id": 1}))
	require.Eventually(t, func() bool { return shadow.Stats().Failed == 1 }, time.Second, 10*time.Millisecond)
	require.NoError(t, shadow.Stop(context.Background()))
}

func TestShadowQueueFullDropsOnlyShadowCopy(t *testing.T) {
	uploader := &blockingUploader{started: make(chan struct{}), release: make(chan struct{})}
	shadow := New(Config{
		Enabled: true, Bucket: "archive", Prefix: "raw", QueueSize: 1,
		BatchSize: 1, FlushInterval: time.Hour, PutTimeout: time.Second,
	}, uploader)
	require.True(t, shadow.Enqueue(DatasetUsage, map[string]any{"id": 1}))
	select {
	case <-uploader.started:
	case <-time.After(time.Second):
		t.Fatal("uploader did not start")
	}
	require.True(t, shadow.Enqueue(DatasetUsage, map[string]any{"id": 2}))
	require.False(t, shadow.Enqueue(DatasetUsage, map[string]any{"id": 3}))
	require.Equal(t, uint64(1), shadow.Stats().Dropped)
	close(uploader.release)
	require.NoError(t, shadow.Stop(context.Background()))
}

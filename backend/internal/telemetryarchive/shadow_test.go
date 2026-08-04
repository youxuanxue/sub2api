package telemetryarchive

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type blockingMarshaler struct {
	started chan struct{}
	release chan struct{}
}

type countingMarshaler struct {
	calls atomic.Int64
}

func (m *countingMarshaler) MarshalJSON() ([]byte, error) {
	m.calls.Add(1)
	return []byte(`{"id":2}`), nil
}

func (m *blockingMarshaler) MarshalJSON() ([]byte, error) {
	close(m.started)
	<-m.release
	return []byte(`{"id":1}`), nil
}

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
		QueueSize: 4, QueueMaxBytes: 1024, MaxEventBytes: 512,
		BatchSize: 2, WorkerCount: 1, FlushInterval: time.Hour, PutTimeout: time.Second,
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
	require.Equal(t, "1", request.Metadata["schema-version"])
	require.Equal(t, "2", request.Metadata["record-count"])
	reader, err := gzip.NewReader(bytes.NewReader(request.Body))
	require.NoError(t, err)
	raw, err := io.ReadAll(reader)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Len(t, lines, 2)
	for idx, line := range lines {
		var record archiveRecord
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		require.Equal(t, SchemaVersion, record.SchemaVersion)
		require.Equal(t, DatasetUsage, record.Dataset)
		require.JSONEq(t, fmt.Sprintf(`{"id":%d}`, idx+1), string(record.Payload))
	}
	stats := shadow.Stats()
	require.Zero(t, stats.Pending)
	require.Zero(t, stats.PendingBytes)
	require.Equal(t, uint64(2), stats.Uploaded)
	require.False(t, stats.LastUploadAt.IsZero())
}

func TestShadowUploadFailureDoesNotAffectEnqueue(t *testing.T) {
	uploader := &fakeUploader{err: errors.New("s3 unavailable")}
	shadow := New(Config{
		Enabled: true, Bucket: "archive", Prefix: "raw", QueueSize: 1,
		QueueMaxBytes: 1024, MaxEventBytes: 512, BatchSize: 1, WorkerCount: 1,
		FlushInterval: time.Hour, PutTimeout: time.Second,
	}, uploader)
	require.True(t, shadow.Enqueue(DatasetOpsError, map[string]any{"id": 1}))
	require.Eventually(t, func() bool { return shadow.Stats().Failed == 1 }, time.Second, 10*time.Millisecond)
	require.NoError(t, shadow.Stop(context.Background()))
}

func TestShadowQueueFullDropsOnlyShadowCopy(t *testing.T) {
	uploader := &blockingUploader{started: make(chan struct{}), release: make(chan struct{})}
	shadow := New(Config{
		Enabled: true, Bucket: "archive", Prefix: "raw", QueueSize: 2,
		QueueMaxBytes: 1024, MaxEventBytes: 512, BatchSize: 1, WorkerCount: 1,
		FlushInterval: time.Hour, PutTimeout: time.Second,
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

func TestShadowSaturationRejectsBeforeJSONMarshal(t *testing.T) {
	uploader := &blockingUploader{started: make(chan struct{}), release: make(chan struct{})}
	shadow := New(Config{
		Enabled: true, Bucket: "archive", Prefix: "raw", QueueSize: 1,
		QueueMaxBytes: 1024, MaxEventBytes: 512, BatchSize: 1, WorkerCount: 1,
		FlushInterval: time.Hour, PutTimeout: time.Second,
	}, uploader)
	require.True(t, shadow.Enqueue(DatasetUsage, map[string]any{"id": 1}))
	select {
	case <-uploader.started:
	case <-time.After(time.Second):
		t.Fatal("uploader did not start")
	}
	value := &countingMarshaler{}
	require.False(t, shadow.Enqueue(DatasetUsage, value))
	require.Zero(t, value.calls.Load())
	close(uploader.release)
	require.NoError(t, shadow.Stop(context.Background()))
}

func TestShadowQueueBytesAndEventSizeAreBounded(t *testing.T) {
	uploader := &blockingUploader{started: make(chan struct{}), release: make(chan struct{})}
	shadow := New(Config{
		Enabled: true, Bucket: "archive", Prefix: "raw", QueueSize: 4,
		QueueMaxBytes: 16, MaxEventBytes: 12, BatchSize: 1, WorkerCount: 1,
		FlushInterval: time.Hour, PutTimeout: time.Second,
	}, uploader)
	require.True(t, shadow.Enqueue(DatasetUsage, json.RawMessage(`{"id":123}`)))
	select {
	case <-uploader.started:
	case <-time.After(time.Second):
		t.Fatal("uploader did not start")
	}
	value := &countingMarshaler{}
	require.False(t, shadow.Enqueue(DatasetUsage, value), "combined in-flight bytes must be bounded")
	require.Zero(t, value.calls.Load(), "byte capacity must be reserved before JSON serialization")
	require.Equal(t, uint64(1), shadow.Stats().Dropped)
	close(uploader.release)
	require.NoError(t, shadow.Stop(context.Background()))
}

func TestShadowRejectsOversizedEventAndReleasesReservation(t *testing.T) {
	shadow := New(Config{
		Enabled: true, Bucket: "archive", Prefix: "raw", QueueSize: 2,
		QueueMaxBytes: 64, MaxEventBytes: 12, BatchSize: 1, WorkerCount: 1,
		FlushInterval: time.Hour, PutTimeout: time.Second,
	}, &fakeUploader{})
	require.False(t, shadow.Enqueue(DatasetUsage, json.RawMessage(`{"payload":"too-large"}`)))
	require.Equal(t, uint64(1), shadow.Stats().Dropped)
	require.Zero(t, shadow.Stats().PendingBytes)
	require.True(t, shadow.Enqueue(DatasetUsage, json.RawMessage(`{"id":1}`)))
	require.NoError(t, shadow.Stop(context.Background()))
}

func TestShadowDoesNotAcceptAfterConcurrentStopCompletes(t *testing.T) {
	shadow := New(Config{
		Enabled: true, Bucket: "archive", Prefix: "raw", QueueSize: 1,
		QueueMaxBytes: 1024, MaxEventBytes: 512, BatchSize: 1, WorkerCount: 1,
		FlushInterval: time.Hour, PutTimeout: time.Second,
	}, &fakeUploader{})
	value := &blockingMarshaler{started: make(chan struct{}), release: make(chan struct{})}
	accepted := make(chan bool, 1)
	go func() {
		accepted <- shadow.Enqueue(DatasetUsage, value)
	}()

	select {
	case <-value.started:
	case <-time.After(time.Second):
		t.Fatal("JSON marshaling did not start")
	}
	require.NoError(t, shadow.Stop(context.Background()))
	close(value.release)
	require.False(t, <-accepted)
	require.Zero(t, shadow.Stats().Enqueued)
}

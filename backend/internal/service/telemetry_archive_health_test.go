package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/telemetryarchive"
	"github.com/stretchr/testify/require"
)

type telemetryHeartbeatCapture struct {
	mu     sync.Mutex
	inputs []*OpsUpsertJobHeartbeatInput
}

func (c *telemetryHeartbeatCapture) UpsertJobHeartbeat(_ context.Context, input *OpsUpsertJobHeartbeatInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := *input
	c.inputs = append(c.inputs, &copy)
	return nil
}

func (c *telemetryHeartbeatCapture) latest() *OpsUpsertJobHeartbeatInput {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.inputs) == 0 {
		return nil
	}
	return c.inputs[len(c.inputs)-1]
}

func TestTelemetryArchiveHealthPublishesCleanAndFailedStats(t *testing.T) {
	uploader := &fakeTelemetryHealthUploader{}
	shadow := telemetryarchive.New(telemetryarchive.Config{
		Enabled: true, Bucket: "archive", Prefix: "raw",
		QueueSize: 2, QueueMaxBytes: 1024, MaxEventBytes: 512,
		BatchSize: 1, WorkerCount: 1, FlushInterval: time.Hour, PutTimeout: time.Second,
	}, uploader)
	repo := &telemetryHeartbeatCapture{}
	health := newTelemetryArchiveHealth(shadow, repo, 10*time.Millisecond, time.Second)
	health.Start()
	require.Eventually(t, func() bool {
		input := repo.latest()
		return input != nil && input.LastSuccessAt != nil && input.LastResult != nil
	}, time.Second, 5*time.Millisecond)
	require.Contains(t, *repo.latest().LastResult, `"dropped":0`)

	uploader.setError(errors.New("s3 unavailable"))
	require.True(t, shadow.Enqueue(telemetryarchive.DatasetUsage, map[string]any{"id": 1}))
	require.Eventually(t, func() bool {
		input := repo.latest()
		return input != nil && input.LastErrorAt != nil && input.LastError != nil
	}, time.Second, 5*time.Millisecond)
	latest := repo.latest()
	require.Equal(t, "dropped=0 failed=1", *latest.LastError)
	require.Contains(t, *latest.LastResult, `"failed":1`)

	require.NoError(t, health.Stop(context.Background()))
	require.NoError(t, shadow.Stop(context.Background()))
}

func TestTelemetryArchiveHealthDisabledHasNoHeartbeat(t *testing.T) {
	repo := &telemetryHeartbeatCapture{}
	health := NewTelemetryArchiveHealth(nil, repo)
	health.Start()
	require.NoError(t, health.Stop(context.Background()))
	require.Nil(t, repo.latest())
}

type fakeTelemetryHealthUploader struct {
	mu  sync.Mutex
	err error
}

func (u *fakeTelemetryHealthUploader) PutObject(_ context.Context, _ telemetryarchive.PutRequest) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.err
}

func (u *fakeTelemetryHealthUploader) setError(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.err = err
}

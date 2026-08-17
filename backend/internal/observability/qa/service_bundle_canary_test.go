//go:build unit

package qa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/bundle"
	"github.com/stretchr/testify/require"
)

type canaryBundleQueue struct {
	enqueue func(context.Context, string) error
}

func (q canaryBundleQueue) Enqueue(ctx context.Context, key string) error {
	return q.enqueue(ctx, key)
}

func TestRunBundleCanaryTraversesSpecQueueWorkerReceiptAndManifest(t *testing.T) {
	objects := archive.NewMemoryObjectStore()
	store := bundle.NewArchiveStore(objects)
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	now := watermark.Add(17 * time.Minute)
	queue := canaryBundleQueue{enqueue: func(ctx context.Context, key string) error {
		specBody, err := store.Read(ctx, key)
		require.NoError(t, err)
		spec, err := bundle.ParseJobSpec(specBody)
		require.NoError(t, err)
		manifest, err := bundle.Publish(ctx, store, bundle.PublishInput{
			Prefix: spec.GenerationPrefix, DataFrom: spec.DataFrom, DataUntil: spec.DataUntil,
			ArchiveWatermark: spec.ArchiveWatermark,
		})
		require.NoError(t, err)
		receipt := bundle.JobReceipt{
			SchemaVersion: bundle.JobSchemaVersion, Kind: bundle.JobKindBundle, JobID: spec.JobID,
			ManifestKey: manifest.ManifestKey, DataFrom: spec.DataFrom, DataUntil: spec.DataUntil,
			ArchiveWatermark: spec.ArchiveWatermark, RecordCount: 0, CompletedAt: now,
		}
		body, err := json.Marshal(receipt)
		require.NoError(t, err)
		_, err = objects.Create(ctx, spec.ReceiptKey, bytes.NewReader(body), int64(len(body)), "application/json")
		return err
	}}

	receipt, err := runBundleCanary(context.Background(), store, queue, func(context.Context) (time.Time, error) {
		return watermark, nil
	}, bundleCanaryOptions{Now: func() time.Time { return now }, Timeout: time.Second, PollInterval: time.Millisecond})
	require.NoError(t, err)
	require.True(t, receipt.OK)
	require.Equal(t, watermark, receipt.ArchiveWatermark)
	require.Equal(t, 24, receipt.CommitCount)
	require.Zero(t, receipt.RecordCount)
	require.NotEmpty(t, receipt.JobID)
}

func TestRunBundleCanaryCreatesANewJobForSameSecondRetries(t *testing.T) {
	store := bundle.NewArchiveStore(archive.NewMemoryObjectStore())
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	base := watermark.Add(17 * time.Minute)
	var keys []string
	queue := canaryBundleQueue{enqueue: func(_ context.Context, key string) error {
		keys = append(keys, key)
		return errors.New("stop after enqueue")
	}}

	for _, observedAt := range []time.Time{base.Add(100 * time.Nanosecond), base.Add(200 * time.Nanosecond)} {
		_, err := runBundleCanary(
			context.Background(),
			store,
			queue,
			func(context.Context) (time.Time, error) { return watermark, nil },
			bundleCanaryOptions{Now: func() time.Time { return observedAt }},
		)
		require.ErrorContains(t, err, "stop after enqueue")
	}
	require.Len(t, keys, 2)
	require.NotEqual(t, keys[0], keys[1])
}

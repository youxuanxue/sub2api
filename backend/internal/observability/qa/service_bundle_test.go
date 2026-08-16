//go:build unit

package qa

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/bundle"
	"github.com/stretchr/testify/require"
)

type recordingBundleQueue struct {
	keys []string
}

func (q *recordingBundleQueue) Enqueue(_ context.Context, key string) error {
	q.keys = append(q.keys, key)
	return nil
}

func TestCreateUserBundleUsesOnlyImmutableS3SpecAndEnqueuesIt(t *testing.T) {
	svc, client, signer := newQAExportTestService(t)
	objects := archive.NewMemoryObjectStore()
	queue := &recordingBundleQueue{}
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	svc.bundleStore = bundle.NewArchiveStore(objects)
	svc.bundleQueue = queue
	svc.bundleWatermark = func(context.Context) (time.Time, error) { return watermark, nil }
	svc.bundleAuthorize = func(context.Context, int64, int64) (bool, error) { return true, nil }
	svc.exportStore = signer

	job, err := svc.CreateUserBundle(context.Background(), 7, 11)
	require.NoError(t, err)
	require.Equal(t, BundleJobPending, job.Status)
	require.Equal(t, watermark.Add(-24*time.Hour), job.DataFrom)
	require.Equal(t, watermark, job.DataUntil)
	require.Len(t, queue.keys, 1)

	count, err := client.QAExportJob.Query().Count(context.Background())
	require.NoError(t, err)
	require.Zero(t, count)

	specBody, err := objects.Get(context.Background(), queue.keys[0])
	require.NoError(t, err)
	spec, err := bundle.ParseJobSpec(specBody)
	require.NoError(t, err)
	require.Equal(t, job.ID, spec.JobID)
	require.Len(t, spec.CommitKeys, 24)
}

func TestUS044_QABundleReadsCommittedManifestAndRejectsCrossUser(t *testing.T) {
	svc, _, signer := newQAExportTestService(t)
	objects := archive.NewMemoryObjectStore()
	store := bundle.NewArchiveStore(objects)
	queue := &recordingBundleQueue{}
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	svc.bundleStore = store
	svc.bundleQueue = queue
	svc.bundleWatermark = func(context.Context) (time.Time, error) { return watermark, nil }
	svc.bundleAuthorize = func(context.Context, int64, int64) (bool, error) { return true, nil }
	svc.exportStore = signer

	pending, err := svc.CreateUserBundle(context.Background(), 7, 11)
	require.NoError(t, err)
	spec := bundle.NewBundleJobSpec(7, 11, watermark)
	manifest, err := bundle.Publish(context.Background(), store, bundle.PublishInput{
		Prefix: spec.GenerationPrefix, DataFrom: spec.DataFrom, DataUntil: spec.DataUntil,
		ArchiveWatermark: spec.ArchiveWatermark,
		Records:          []bundle.Record{{RequestID: "req-1", UserID: 7, APIKeyID: 11, CapturedAt: spec.DataFrom.Add(time.Minute)}},
	})
	require.NoError(t, err)
	receipt := bundle.JobReceipt{
		SchemaVersion: bundle.JobSchemaVersion, Kind: bundle.JobKindBundle, JobID: spec.JobID,
		ManifestKey: manifest.ManifestKey, DataFrom: spec.DataFrom, DataUntil: spec.DataUntil,
		ArchiveWatermark: spec.ArchiveWatermark, RecordCount: 1, CompletedAt: watermark.Add(time.Minute),
	}
	receiptBody, err := json.Marshal(receipt)
	require.NoError(t, err)
	_, err = objects.Create(context.Background(), spec.ReceiptKey, bytes.NewReader(receiptBody), int64(len(receiptBody)), "application/json")
	require.NoError(t, err)

	ready, found, err := svc.GetUserBundle(context.Background(), 7, pending.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, BundleJobReady, ready.Status)
	require.Equal(t, 1, ready.RecordCount)
	require.Len(t, ready.Pages, 1)
	require.Contains(t, ready.Pages[0].URL, manifest.Pages[0].Key)

	_, found, err = svc.GetUserBundle(context.Background(), 8, pending.ID)
	require.NoError(t, err)
	require.False(t, found)
}

func TestGetUserBundleRejectsTamperedS3RegistrySpec(t *testing.T) {
	svc, _, signer := newQAExportTestService(t)
	objects := archive.NewMemoryObjectStore()
	store := bundle.NewArchiveStore(objects)
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	spec := bundle.NewBundleJobSpec(7, 11, watermark)
	tampered := spec
	tampered.APIKeyID = 12
	body, err := json.Marshal(tampered)
	require.NoError(t, err)
	_, err = objects.Create(context.Background(), spec.SpecKey, bytes.NewReader(body), int64(len(body)), "application/json")
	require.NoError(t, err)

	svc.bundleStore = store
	svc.bundleAuthorize = func(context.Context, int64, int64) (bool, error) {
		t.Fatal("tampered spec must fail before authorization")
		return false, nil
	}
	svc.exportStore = signer

	_, found, err := svc.GetUserBundle(context.Background(), 7, spec.JobID)
	require.ErrorContains(t, err, "qa bundle build job is invalid")
	require.False(t, found)
}

func TestGetUserBundleTreatsExpiredS3RegistrySpecAsMissing(t *testing.T) {
	svc, client, signer := newQAExportTestService(t)
	svc.bundleStore = bundle.NewArchiveStore(archive.NewMemoryObjectStore())
	svc.bundleAuthorize = func(context.Context, int64, int64) (bool, error) {
		t.Fatal("missing S3 registry spec must fail before authorization")
		return false, nil
	}
	svc.exportStore = signer

	_, found, err := svc.GetUserBundle(context.Background(), 7, strings.Repeat("a", 64))
	require.NoError(t, err)
	require.False(t, found)
	count, err := client.QAExportJob.Query().Count(context.Background())
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestCreateUserBundleExportRequiresReadyBundleAndSignsWorkerArtifact(t *testing.T) {
	svc, client, signer := newQAExportTestService(t)
	objects := archive.NewMemoryObjectStore()
	store := bundle.NewArchiveStore(objects)
	queue := &recordingBundleQueue{}
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	svc.bundleStore = store
	svc.bundleQueue = queue
	svc.bundleWatermark = func(context.Context) (time.Time, error) { return watermark, nil }
	svc.bundleAuthorize = func(context.Context, int64, int64) (bool, error) { return true, nil }
	svc.exportStore = signer

	bundleJob, err := svc.CreateUserBundle(context.Background(), 7, 11)
	require.NoError(t, err)
	_, err = svc.CreateUserBundleExport(context.Background(), 7, bundleJob.ID)
	require.Error(t, err)
	require.Len(t, queue.keys, 1, "unready bundle must not enqueue zip")

	spec := bundle.NewBundleJobSpec(7, 11, watermark)
	manifest, err := bundle.Publish(context.Background(), store, bundle.PublishInput{
		Prefix: spec.GenerationPrefix, DataFrom: spec.DataFrom, DataUntil: spec.DataUntil,
		ArchiveWatermark: spec.ArchiveWatermark,
	})
	require.NoError(t, err)
	publishBundleReceipt(t, objects, spec, bundle.JobReceipt{
		SchemaVersion: bundle.JobSchemaVersion, Kind: bundle.JobKindBundle, JobID: spec.JobID,
		ManifestKey: manifest.ManifestKey, DataFrom: spec.DataFrom, DataUntil: spec.DataUntil,
		ArchiveWatermark: spec.ArchiveWatermark, CompletedAt: watermark.Add(time.Minute),
	})

	exportJob, err := svc.CreateUserBundleExport(context.Background(), 7, bundleJob.ID)
	require.NoError(t, err)
	require.Equal(t, BundleJobPending, exportJob.Status)
	require.Len(t, queue.keys, 2)
	count, err := client.QAExportJob.Query().Count(context.Background())
	require.NoError(t, err)
	require.Zero(t, count, "Bundle and ZIP registry ownership must remain S3-only")
	zipSpecBody, err := objects.Get(context.Background(), queue.keys[1])
	require.NoError(t, err)
	zipSpec, err := bundle.ParseJobSpec(zipSpecBody)
	require.NoError(t, err)
	require.Equal(t, bundle.JobKindBundleZip, zipSpec.Kind)

	zipReceipt := bundle.JobReceipt{
		SchemaVersion: bundle.JobSchemaVersion, Kind: bundle.JobKindBundleZip, JobID: zipSpec.JobID,
		ManifestKey: zipSpec.ManifestKey, StorageKey: zipSpec.OutputKey,
		DataFrom: zipSpec.DataFrom, DataUntil: zipSpec.DataUntil, ArchiveWatermark: zipSpec.ArchiveWatermark,
		RecordCount: 3, SHA256: strings.Repeat("a", 64), CompletedAt: watermark.Add(2 * time.Minute),
	}
	publishBundleReceipt(t, objects, zipSpec, zipReceipt)
	ready, found, err := svc.GetUserBundleExport(context.Background(), 7, zipSpec.JobID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, BundleJobReady, ready.Status)
	require.Contains(t, ready.DownloadURL, zipSpec.OutputKey)
	count, err = client.QAExportJob.Query().Count(context.Background())
	require.NoError(t, err)
	require.Zero(t, count, "reading a completed ZIP must not require or recreate a database job row")

	_, found, err = svc.GetUserBundleExport(context.Background(), 8, zipSpec.JobID)
	require.NoError(t, err)
	require.False(t, found)
}

func publishBundleReceipt(t *testing.T, objects *archive.MemoryObjectStore, spec bundle.JobSpec, receipt bundle.JobReceipt) {
	t.Helper()
	body, err := json.Marshal(receipt)
	require.NoError(t, err)
	_, err = objects.Create(context.Background(), spec.ReceiptKey, bytes.NewReader(body), int64(len(body)), "application/json")
	require.NoError(t, err)
}

func TestLatestCompleteBundleWatermarkReadsOnlyContinuousArchiveControl(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	want := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery("WITH eligible AS").WillReturnRows(sqlmock.NewRows([]string{"watermark"}).AddRow(want))

	got, err := latestCompleteBundleWatermark(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

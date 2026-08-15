package qa

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/qaexportjob"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/bundle"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

func (s *Service) UserTrajExportEnabled(ctx context.Context, userID int64) (bool, error) {
	user, err := s.client.User.Get(ctx, userID)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return user.TrajExportEnabled, nil
}

func (s *Service) UserMayExportAPIKey(ctx context.Context, userID, apiKeyID int64) (bool, error) {
	key, err := s.client.APIKey.Query().Where(
		apikey.IDEQ(apiKeyID), apikey.UserIDEQ(userID), apikey.DeletedAtIsNil(),
	).WithGroup().Only(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	group := key.Edges.Group
	return group != nil && slices.Contains(engine.TrajProjectablePlatforms(), group.Platform), nil
}

func (s *Service) configureBundle(ctx context.Context, cfg *config.Config, db *sql.DB) error {
	if cfg == nil || !cfg.QaBundle.Enabled {
		return nil
	}
	objectStore, err := archive.NewObjectStoreFromConfig(ctx, cfg.QaBundle.Storage)
	if err != nil {
		return fmt.Errorf("configure qa bundle object store: %w", err)
	}
	signer, err := newBlobStore(config.QACaptureConfig{Storage: cfg.QaBundle.Storage})
	if err != nil {
		return fmt.Errorf("configure qa bundle signer: %w", err)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, qaS3LoadOptions(cfg.QaBundle.Storage)...)
	if err != nil {
		return fmt.Errorf("configure qa bundle queue credentials: %w", err)
	}
	queue, err := bundle.NewSQSJobQueue(sqs.NewFromConfig(awsCfg), cfg.QaBundle.QueueURL)
	if err != nil {
		return err
	}
	s.bundleStore = bundle.NewArchiveStore(objectStore)
	s.bundleQueue = queue
	s.bundleWatermark = func(queryCtx context.Context) (time.Time, error) {
		return latestCompleteBundleWatermark(queryCtx, db)
	}
	s.bundleAuthorize = s.UserMayExportAPIKey
	s.exportStore = signer
	return nil
}

func latestCompleteBundleWatermark(ctx context.Context, db *sql.DB) (time.Time, error) {
	if db == nil {
		return time.Time{}, errors.New("qa bundle archive control database is unavailable")
	}
	var watermark time.Time
	err := db.QueryRowContext(ctx, `
WITH eligible AS (
    SELECT window_start, window_end
    FROM qa_archive_shards
    WHERE generation = 0
      AND state = 'committed'
      AND restore_verified_at IS NOT NULL
      AND verification_error_code IS NULL
), candidates AS (
    SELECT window_end AS watermark
    FROM eligible
    WHERE window_end <= date_trunc('hour', clock_timestamp())
)
SELECT candidate.watermark
FROM candidates candidate
WHERE NOT EXISTS (
    SELECT 1
    FROM generate_series(
        candidate.watermark - interval '24 hours',
        candidate.watermark - interval '1 hour',
        interval '1 hour'
    ) AS required(window_start)
    WHERE NOT EXISTS (
        SELECT 1
        FROM eligible hour
        WHERE hour.window_start = required.window_start
          AND hour.window_end = required.window_start + interval '1 hour'
    )
)
ORDER BY candidate.watermark DESC
LIMIT 1`).Scan(&watermark)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, errors.New("no continuous 24-hour verified QA archive watermark")
	}
	if err != nil {
		return time.Time{}, err
	}
	return watermark.UTC(), nil
}

type BundleJobStatus string

const (
	BundleJobPending BundleJobStatus = "pending"
	BundleJobReady   BundleJobStatus = "ready"
	BundleJobFailed  BundleJobStatus = "failed"
)

type BundlePageAccess struct {
	Page        int    `json:"page"`
	RecordCount int    `json:"record_count"`
	SHA256      string `json:"sha256"`
	URL         string `json:"url"`
}

type BundleJob struct {
	ID               string             `json:"job_id"`
	Status           BundleJobStatus    `json:"status"`
	APIKeyID         int64              `json:"api_key_id"`
	DataFrom         time.Time          `json:"data_from"`
	DataUntil        time.Time          `json:"data_until"`
	ArchiveWatermark time.Time          `json:"archive_watermark"`
	RecordCount      int                `json:"record_count"`
	Pages            []BundlePageAccess `json:"pages,omitempty"`
	Error            string             `json:"error,omitempty"`
}

type BundleExportJob struct {
	ID          string          `json:"job_id"`
	BundleJobID string          `json:"bundle_job_id"`
	Status      BundleJobStatus `json:"status"`
	RecordCount int             `json:"record_count"`
	DownloadURL string          `json:"download_url,omitempty"`
	ExpiresAt   time.Time       `json:"expires_at,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func (s *Service) CreateUserBundle(ctx context.Context, userID, apiKeyID int64) (BundleJob, error) {
	if s == nil || s.client == nil || s.bundleStore == nil || s.bundleQueue == nil || s.bundleWatermark == nil || s.bundleAuthorize == nil {
		return BundleJob{}, errors.New("qa bundle service is unavailable")
	}
	authorized, err := s.bundleAuthorize(ctx, userID, apiKeyID)
	if err != nil {
		return BundleJob{}, err
	}
	if !authorized {
		return BundleJob{}, errors.New("qa bundle API key is not authorized")
	}
	watermark, err := s.bundleWatermark(ctx)
	if err != nil {
		return BundleJob{}, fmt.Errorf("select qa bundle watermark: %w", err)
	}
	spec := bundle.NewBundleJobSpec(userID, apiKeyID, watermark)
	if err := bundle.PublishJobSpec(ctx, s.bundleStore, spec); err != nil {
		return BundleJob{}, fmt.Errorf("publish qa bundle job spec: %w", err)
	}
	row, err := s.client.QAExportJob.Query().Where(qaexportjob.JobIDEQ(spec.JobID)).Only(ctx)
	if ent.IsNotFound(err) {
		row, err = s.client.QAExportJob.Create().
			SetJobID(spec.JobID).SetUserID(userID).SetAPIKeyID(apiKeyID).
			SetStatus(string(BundleJobPending)).SetExportKind(string(bundle.JobKindBundle)).SetFormat(bundle.JobSchemaVersion).
			SetWindowStart(spec.DataFrom).SetWindowEnd(spec.DataUntil).SetStorageKey(spec.ManifestKey).
			Save(ctx)
	}
	if err != nil {
		return BundleJob{}, err
	}
	if row.UserID != userID || row.APIKeyID == nil || *row.APIKeyID != apiKeyID || row.ExportKind != string(bundle.JobKindBundle) {
		return BundleJob{}, errors.New("qa bundle deterministic job ownership conflict")
	}
	ready, err := s.bundleStore.Head(ctx, spec.ReceiptKey)
	if err != nil {
		return BundleJob{}, err
	}
	if !ready {
		if err := s.bundleQueue.Enqueue(ctx, spec.SpecKey); err != nil {
			return BundleJob{}, fmt.Errorf("enqueue qa bundle job: %w", err)
		}
	}
	job, found, err := s.GetUserBundle(ctx, userID, spec.JobID)
	if err != nil || !found {
		return BundleJob{}, err
	}
	return job, nil
}

func (s *Service) GetUserBundle(ctx context.Context, userID int64, jobID string) (BundleJob, bool, error) {
	if s == nil || s.client == nil || s.bundleStore == nil {
		return BundleJob{}, false, errors.New("qa bundle service is unavailable")
	}
	row, err := s.client.QAExportJob.Query().Where(
		qaexportjob.JobIDEQ(strings.TrimSpace(jobID)),
		qaexportjob.UserIDEQ(userID),
		qaexportjob.ExportKindEQ(string(bundle.JobKindBundle)),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return BundleJob{}, false, nil
	}
	if err != nil {
		return BundleJob{}, false, err
	}
	if row.APIKeyID == nil || row.WindowStart == nil || row.WindowEnd == nil {
		return BundleJob{}, false, errors.New("qa bundle job row is incomplete")
	}
	if s.bundleAuthorize == nil {
		return BundleJob{}, false, errors.New("qa bundle authorization is unavailable")
	}
	authorized, err := s.bundleAuthorize(ctx, row.UserID, *row.APIKeyID)
	if err != nil {
		return BundleJob{}, false, err
	}
	if !authorized {
		return BundleJob{}, false, nil
	}
	spec := bundle.NewBundleJobSpec(row.UserID, *row.APIKeyID, row.WindowEnd.UTC())
	if spec.JobID != row.JobID || !spec.DataFrom.Equal(row.WindowStart.UTC()) || spec.ManifestKey != row.StorageKey {
		return BundleJob{}, false, errors.New("qa bundle job row does not match canonical spec")
	}
	job := BundleJob{
		ID: spec.JobID, Status: BundleJobPending, APIKeyID: spec.APIKeyID,
		DataFrom: spec.DataFrom, DataUntil: spec.DataUntil, ArchiveWatermark: spec.ArchiveWatermark,
	}
	ready, err := s.bundleStore.Head(ctx, spec.ReceiptKey)
	if err != nil {
		return BundleJob{}, false, err
	}
	if !ready {
		failed, headErr := s.bundleStore.Head(ctx, spec.FailureKey)
		if headErr != nil {
			return BundleJob{}, false, headErr
		}
		if failed {
			job.Status = BundleJobFailed
			job.Error = "bundle_failed"
		}
		return job, true, nil
	}
	receiptBody, err := s.bundleStore.Read(ctx, spec.ReceiptKey)
	if err != nil {
		return BundleJob{}, false, err
	}
	var receipt bundle.JobReceipt
	if err := json.Unmarshal(receiptBody, &receipt); err != nil {
		return BundleJob{}, false, errors.New("qa bundle receipt is invalid")
	}
	if receipt.SchemaVersion != bundle.JobSchemaVersion || receipt.Kind != bundle.JobKindBundle || receipt.JobID != spec.JobID ||
		receipt.ManifestKey != spec.ManifestKey || !receipt.DataFrom.Equal(spec.DataFrom) || !receipt.DataUntil.Equal(spec.DataUntil) ||
		!receipt.ArchiveWatermark.Equal(spec.ArchiveWatermark) || receipt.RecordCount < 0 {
		return BundleJob{}, false, errors.New("qa bundle receipt does not match job")
	}
	manifestBody, err := s.bundleStore.Read(ctx, spec.ManifestKey)
	if err != nil {
		return BundleJob{}, false, err
	}
	var manifest bundle.Manifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil || manifest.SchemaVersion != bundle.SchemaVersion ||
		manifest.RecordCount != receipt.RecordCount || !manifest.DataFrom.Equal(spec.DataFrom) || !manifest.DataUntil.Equal(spec.DataUntil) ||
		!manifest.ArchiveWatermark.Equal(spec.ArchiveWatermark) {
		return BundleJob{}, false, errors.New("qa bundle manifest does not match receipt")
	}
	job.Status = BundleJobReady
	job.RecordCount = receipt.RecordCount
	for index, page := range manifest.Pages {
		if page.Page != index+1 || !strings.HasPrefix(page.Key, spec.GenerationPrefix+"/pages/") {
			return BundleJob{}, false, errors.New("qa bundle page is outside job generation")
		}
		if s.exportStore == nil {
			return BundleJob{}, false, errors.New("qa bundle signer is unavailable")
		}
		url, err := s.exportStore.PresignURL(ctx, page.Key, presignedURLTTL)
		if err != nil {
			return BundleJob{}, false, err
		}
		job.Pages = append(job.Pages, BundlePageAccess{Page: page.Page, RecordCount: page.RecordCount, SHA256: page.SHA256, URL: url})
	}
	return job, true, nil
}

func (s *Service) CreateUserBundleExport(ctx context.Context, userID int64, bundleJobID string) (BundleExportJob, error) {
	bundleJob, found, err := s.GetUserBundle(ctx, userID, bundleJobID)
	if err != nil {
		return BundleExportJob{}, err
	}
	if !found || bundleJob.Status != BundleJobReady {
		return BundleExportJob{}, errors.New("qa bundle is not ready for export")
	}
	bundleSpec := bundle.NewBundleJobSpec(userID, bundleJob.APIKeyID, bundleJob.ArchiveWatermark)
	zipSpec := bundle.NewZipJobSpec(bundleSpec, bundleSpec.ManifestKey)
	if err := bundle.PublishJobSpec(ctx, s.bundleStore, zipSpec); err != nil {
		return BundleExportJob{}, fmt.Errorf("publish qa bundle zip job spec: %w", err)
	}
	row, err := s.client.QAExportJob.Query().Where(qaexportjob.JobIDEQ(zipSpec.JobID)).Only(ctx)
	if ent.IsNotFound(err) {
		row, err = s.client.QAExportJob.Create().
			SetJobID(zipSpec.JobID).SetUserID(userID).SetAPIKeyID(bundleJob.APIKeyID).
			SetStatus(string(BundleJobPending)).SetExportKind(string(bundle.JobKindBundleZip)).SetFormat("zip").
			SetWindowStart(zipSpec.DataFrom).SetWindowEnd(zipSpec.DataUntil).SetStorageKey(zipSpec.OutputKey).
			Save(ctx)
	}
	if err != nil {
		return BundleExportJob{}, err
	}
	if row.UserID != userID || row.APIKeyID == nil || *row.APIKeyID != bundleJob.APIKeyID || row.ExportKind != string(bundle.JobKindBundleZip) {
		return BundleExportJob{}, errors.New("qa bundle export deterministic job ownership conflict")
	}
	ready, err := s.bundleStore.Head(ctx, zipSpec.ReceiptKey)
	if err != nil {
		return BundleExportJob{}, err
	}
	if !ready {
		if err := s.bundleQueue.Enqueue(ctx, zipSpec.SpecKey); err != nil {
			return BundleExportJob{}, fmt.Errorf("enqueue qa bundle zip job: %w", err)
		}
	}
	job, found, err := s.GetUserBundleExport(ctx, userID, zipSpec.JobID)
	if err != nil || !found {
		return BundleExportJob{}, err
	}
	return job, nil
}

func (s *Service) GetUserBundleExport(ctx context.Context, userID int64, jobID string) (BundleExportJob, bool, error) {
	if s == nil || s.client == nil || s.bundleStore == nil {
		return BundleExportJob{}, false, errors.New("qa bundle service is unavailable")
	}
	row, err := s.client.QAExportJob.Query().Where(
		qaexportjob.JobIDEQ(strings.TrimSpace(jobID)),
		qaexportjob.UserIDEQ(userID),
		qaexportjob.ExportKindEQ(string(bundle.JobKindBundleZip)),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return BundleExportJob{}, false, nil
	}
	if err != nil {
		return BundleExportJob{}, false, err
	}
	if row.APIKeyID == nil || row.WindowStart == nil || row.WindowEnd == nil {
		return BundleExportJob{}, false, errors.New("qa bundle export row is incomplete")
	}
	if s.bundleAuthorize == nil {
		return BundleExportJob{}, false, errors.New("qa bundle authorization is unavailable")
	}
	authorized, err := s.bundleAuthorize(ctx, row.UserID, *row.APIKeyID)
	if err != nil {
		return BundleExportJob{}, false, err
	}
	if !authorized {
		return BundleExportJob{}, false, nil
	}
	bundleSpec := bundle.NewBundleJobSpec(row.UserID, *row.APIKeyID, row.WindowEnd.UTC())
	zipSpec := bundle.NewZipJobSpec(bundleSpec, bundleSpec.ManifestKey)
	if zipSpec.JobID != row.JobID || !zipSpec.DataFrom.Equal(row.WindowStart.UTC()) || zipSpec.OutputKey != row.StorageKey {
		return BundleExportJob{}, false, errors.New("qa bundle export row does not match canonical spec")
	}
	job := BundleExportJob{ID: zipSpec.JobID, BundleJobID: bundleSpec.JobID, Status: BundleJobPending}
	ready, err := s.bundleStore.Head(ctx, zipSpec.ReceiptKey)
	if err != nil {
		return BundleExportJob{}, false, err
	}
	if !ready {
		failed, headErr := s.bundleStore.Head(ctx, zipSpec.FailureKey)
		if headErr != nil {
			return BundleExportJob{}, false, headErr
		}
		if failed {
			job.Status = BundleJobFailed
			job.Error = "export_failed"
		}
		return job, true, nil
	}
	body, err := s.bundleStore.Read(ctx, zipSpec.ReceiptKey)
	if err != nil {
		return BundleExportJob{}, false, err
	}
	var receipt bundle.JobReceipt
	if err := json.Unmarshal(body, &receipt); err != nil || receipt.SchemaVersion != bundle.JobSchemaVersion ||
		receipt.Kind != bundle.JobKindBundleZip || receipt.JobID != zipSpec.JobID || receipt.ManifestKey != zipSpec.ManifestKey ||
		receipt.StorageKey != zipSpec.OutputKey || !receipt.DataFrom.Equal(zipSpec.DataFrom) || !receipt.DataUntil.Equal(zipSpec.DataUntil) ||
		!receipt.ArchiveWatermark.Equal(zipSpec.ArchiveWatermark) || receipt.RecordCount < 0 {
		return BundleExportJob{}, false, errors.New("qa bundle export receipt does not match job")
	}
	if s.exportStore == nil {
		return BundleExportJob{}, false, errors.New("qa bundle signer is unavailable")
	}
	url, err := s.exportStore.PresignURL(ctx, zipSpec.OutputKey, presignedURLTTL)
	if err != nil {
		return BundleExportJob{}, false, err
	}
	job.Status = BundleJobReady
	job.RecordCount = receipt.RecordCount
	job.DownloadURL = url
	job.ExpiresAt = time.Now().UTC().Add(presignedURLTTL)
	return job, true, nil
}

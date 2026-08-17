package qa

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/bundle"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const bundleCanarySchemaVersion = "qa-bundle-canary-v1"

type BundleCanaryReceipt struct {
	SchemaVersion    string    `json:"schema_version"`
	OK               bool      `json:"ok"`
	JobID            string    `json:"job_id"`
	ArchiveWatermark time.Time `json:"archive_watermark"`
	CommitCount      int       `json:"commit_count"`
	RecordCount      int       `json:"record_count"`
	CompletedAt      time.Time `json:"completed_at"`
}

type bundleCanaryOptions struct {
	Now          func() time.Time
	Timeout      time.Duration
	PollInterval time.Duration
}

// RunBundleCanary traverses the same S3 spec, SQS, worker, receipt, and manifest
// path used by users. Synthetic identities intentionally project zero records.
func RunBundleCanary(
	ctx context.Context,
	cfg *config.Config,
	db *sql.DB,
	timeout time.Duration,
) (BundleCanaryReceipt, error) {
	if cfg == nil || !cfg.QaBundle.Enabled {
		return BundleCanaryReceipt{}, errors.New("qa bundle canary requires QA_BUNDLE_ENABLED=true")
	}
	objectStore, err := archive.NewObjectStoreFromConfig(ctx, cfg.QaBundle.Storage)
	if err != nil {
		return BundleCanaryReceipt{}, fmt.Errorf("open qa bundle canary object store: %w", err)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, qaS3LoadOptions(cfg.QaBundle.Storage)...)
	if err != nil {
		return BundleCanaryReceipt{}, fmt.Errorf("load qa bundle canary queue credentials: %w", err)
	}
	queue, err := bundle.NewSQSJobQueue(sqs.NewFromConfig(awsCfg), cfg.QaBundle.QueueURL)
	if err != nil {
		return BundleCanaryReceipt{}, err
	}
	return runBundleCanary(
		ctx,
		bundle.NewArchiveStore(objectStore),
		queue,
		func(queryCtx context.Context) (time.Time, error) { return latestCompleteBundleWatermark(queryCtx, db) },
		bundleCanaryOptions{Now: time.Now, Timeout: timeout, PollInterval: time.Second},
	)
}

func runBundleCanary(
	ctx context.Context,
	store bundle.Store,
	queue bundle.JobQueue,
	watermark func(context.Context) (time.Time, error),
	opts bundleCanaryOptions,
) (BundleCanaryReceipt, error) {
	var result BundleCanaryReceipt
	if store == nil || queue == nil || watermark == nil {
		return result, errors.New("qa bundle canary dependencies are incomplete")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	observedAt := opts.Now().UTC()
	archiveWatermark, err := watermark(ctx)
	if err != nil {
		return result, fmt.Errorf("select qa bundle canary watermark: %w", err)
	}
	const syntheticUserID int64 = 7_000_000_000_000_000_001
	syntheticAPIKeyID := int64(7_100_000_000_000_000_000) + observedAt.UnixNano()%100_000_000_000_000_000
	spec := bundle.NewBundleJobSpec(syntheticUserID, syntheticAPIKeyID, archiveWatermark)
	if err := bundle.PublishJobSpec(ctx, store, spec); err != nil {
		return result, fmt.Errorf("publish qa bundle canary spec: %w", err)
	}
	if err := queue.Enqueue(ctx, spec.SpecKey); err != nil {
		return result, fmt.Errorf("enqueue qa bundle canary: %w", err)
	}
	deadline := time.NewTimer(opts.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		failed, err := store.Head(ctx, spec.FailureKey)
		if err != nil {
			return result, err
		}
		if failed {
			return result, errors.New("qa bundle canary worker reported failure")
		}
		ready, err := store.Head(ctx, spec.ReceiptKey)
		if err != nil {
			return result, err
		}
		if ready {
			return validateBundleCanaryResult(ctx, store, spec, opts.Now().UTC())
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-deadline.C:
			return result, errors.New("qa bundle canary timed out")
		case <-ticker.C:
		}
	}
}

func validateBundleCanaryResult(
	ctx context.Context,
	store bundle.Store,
	spec bundle.JobSpec,
	completedAt time.Time,
) (BundleCanaryReceipt, error) {
	body, err := store.Read(ctx, spec.ReceiptKey)
	if err != nil {
		return BundleCanaryReceipt{}, err
	}
	var worker bundle.JobReceipt
	if err := json.Unmarshal(body, &worker); err != nil || worker.SchemaVersion != bundle.JobSchemaVersion ||
		worker.Kind != bundle.JobKindBundle || worker.JobID != spec.JobID || worker.ManifestKey != spec.ManifestKey ||
		!worker.DataFrom.Equal(spec.DataFrom) || !worker.DataUntil.Equal(spec.DataUntil) ||
		!worker.ArchiveWatermark.Equal(spec.ArchiveWatermark) || worker.RecordCount != 0 {
		return BundleCanaryReceipt{}, errors.New("qa bundle canary receipt does not match the synthetic job")
	}
	manifestBody, err := store.Read(ctx, spec.ManifestKey)
	if err != nil {
		return BundleCanaryReceipt{}, err
	}
	var manifest bundle.Manifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil || manifest.SchemaVersion != bundle.SchemaVersion ||
		manifest.RecordCount != 0 || len(manifest.Pages) != 0 || !manifest.DataFrom.Equal(spec.DataFrom) ||
		!manifest.DataUntil.Equal(spec.DataUntil) || !manifest.ArchiveWatermark.Equal(spec.ArchiveWatermark) {
		return BundleCanaryReceipt{}, errors.New("qa bundle canary manifest does not match the synthetic job")
	}
	return BundleCanaryReceipt{
		SchemaVersion: bundleCanarySchemaVersion, OK: true, JobID: spec.JobID,
		ArchiveWatermark: spec.ArchiveWatermark, CommitCount: len(spec.CommitKeys),
		RecordCount: worker.RecordCount, CompletedAt: completedAt,
	}, nil
}

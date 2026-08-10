package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	qaMaintenanceConfirmation      = "tokenkey-prod-qa-maintenance-v1"
	qaMaintenanceReceiptVersion    = 2
	qaMaintenanceReceiptMode       = "qa_maintenance_archive_only"
	qaMaintenanceReceiptModeUpload = "qa_maintenance_archive"
	qaMaintenanceJobName           = "qa-maintenance"
	qaMaintenanceAdvisoryLockID    = archive.MaintenanceAdvisoryLockID
	qaMaintenanceHeartbeatTimeout  = 5 * time.Second
	qaMaintenanceSourceRetention   = 24 * time.Hour
)

type qaMaintenanceDeps struct {
	loadConfig      func() (*config.Config, error)
	openDB          func(driverName, dataSourceName string) (*sql.DB, error)
	tryAdvisoryLock func(context.Context, *sql.Conn) (bool, error)
	unlockAdvisory  func(context.Context, *sql.Conn) error
	planShard       func(context.Context, *sql.Conn, time.Time, time.Time, string, bool) (qaMaintenancePlan, error)
	reconcileShard  func(context.Context, *sql.Conn, archive.ObjectStore, archive.Window, string, string) (archive.ReconcileReceipt, error)
	selectOldest    func(context.Context, *sql.Conn, archive.Window, time.Time) (archive.CatchupSelection, bool, error)
	newObjectStore  func(context.Context, config.QACaptureStorageConfig) (archive.ObjectStore, error)
	writeHeartbeat  func(context.Context, *sql.DB, *service.OpsUpsertJobHeartbeatInput) error
	now             func() time.Time
}

type qaMaintenancePlan struct {
	WindowStart           time.Time `json:"window_start"`
	WindowEnd             time.Time `json:"window_end"`
	S3Prefix              string    `json:"s3_prefix"`
	RecordCount           int64     `json:"record_count"`
	BlobRefCount          int64     `json:"blob_ref_count"`
	AggregateRecordCount  int64     `json:"aggregate_record_count"`
	ArchiveEnabled        bool      `json:"archive_enabled"`
	Uploaded              bool      `json:"uploaded"`
	SegmentID             string    `json:"segment_id,omitempty"`
	SegmentCount          int       `json:"segment_count,omitempty"`
	CommitKey             string    `json:"commit_key,omitempty"`
	CommitETag            string    `json:"commit_etag,omitempty"`
	BlobPresentCount      int64     `json:"blob_present_count"`
	BlobMissingCount      int64     `json:"blob_missing_count"`
	RestoreVerified       bool      `json:"restore_verified"`
	State                 string    `json:"state,omitempty"`
	VerificationErrorCode string    `json:"verification_error_code,omitempty"`
	CleanupEligible       bool      `json:"cleanup_eligible"`
}

type qaMaintenanceCycleDeps struct {
	reconcile    func(context.Context, archive.Window) (archive.ReconcileReceipt, error)
	selectOldest func(context.Context, archive.Window, time.Time) (archive.CatchupSelection, bool, error)
}

type qaMaintenanceCycleResult struct {
	Normal                archive.ReconcileReceipt
	CompensationSelection *archive.CatchupSelection
	Compensation          *archive.ReconcileReceipt
	FailureStage          string
	FailureCode           string
}

func runQAMaintenanceArchiveCycle(
	ctx context.Context,
	normal archive.Window,
	retentionCutoff time.Time,
	deps qaMaintenanceCycleDeps,
) (qaMaintenanceCycleResult, error) {
	var result qaMaintenanceCycleResult
	if deps.reconcile == nil || deps.selectOldest == nil {
		return failQAMaintenanceCycle(result, "cycle_preflight", "dependencies_incomplete", fmt.Errorf("qa maintenance archive cycle dependencies are incomplete"))
	}
	normalReceipt, err := deps.reconcile(ctx, normal)
	if err != nil {
		return failQAMaintenanceCycle(result, "normal_reconcile", qaMaintenanceArchiveErrorCode(err), fmt.Errorf("normal reconcile: %w", err))
	}
	if err := validateQAMaintenanceReceipt("normal", normal, normalReceipt); err != nil {
		return failQAMaintenanceCycle(result, "normal_validate", "invalid_reconcile_receipt", err)
	}
	result.Normal = normalReceipt

	selection, ok, err := deps.selectOldest(ctx, normal, retentionCutoff.UTC())
	if err != nil {
		return failQAMaintenanceCycle(result, "compensation_select", "compensation_selection_failed", fmt.Errorf("select compensation: %w", err))
	}
	if !ok {
		return result, nil
	}
	result.CompensationSelection = &selection
	if selection.Disposition == archive.CatchupDispositionSourceUnavailableAfterRetention {
		return failQAMaintenanceCycle(
			result,
			"compensation_terminal",
			archive.IntegritySourceUnavailableAfterRetention,
			fmt.Errorf(
				"compensation %s: source unavailable after retention",
				selection.Window.Start.Format(time.RFC3339),
			),
		)
	}
	if selection.Disposition != archive.CatchupDispositionReconcile {
		return failQAMaintenanceCycle(
			result,
			"compensation_select",
			"invalid_compensation_disposition",
			fmt.Errorf("compensation %s: unsupported disposition %q", selection.Window.Start.Format(time.RFC3339), selection.Disposition),
		)
	}
	compensation, err := deps.reconcile(ctx, selection.Window)
	if err != nil {
		return failQAMaintenanceCycle(result, "compensation_reconcile", qaMaintenanceArchiveErrorCode(err), fmt.Errorf("compensation reconcile: %w", err))
	}
	if err := validateQAMaintenanceReceipt("compensation", selection.Window, compensation); err != nil {
		return failQAMaintenanceCycle(result, "compensation_validate", "invalid_reconcile_receipt", err)
	}
	result.Compensation = &compensation
	return result, nil
}

func failQAMaintenanceCycle(result qaMaintenanceCycleResult, stage, code string, err error) (qaMaintenanceCycleResult, error) {
	result.FailureStage = stage
	result.FailureCode = code
	return result, err
}

func qaMaintenanceArchiveErrorCode(err error) string {
	var integrity *archive.IntegrityError
	if errors.As(err, &integrity) && strings.TrimSpace(integrity.Code) != "" {
		return integrity.Code
	}
	if errors.Is(err, archive.ErrPreconditionFailed) {
		return "commit_conflict"
	}
	return "archive_failed"
}

func validateQAMaintenanceReceipt(stage string, window archive.Window, receipt archive.ReconcileReceipt) error {
	if !receipt.WindowStart.Equal(window.Start) || !receipt.WindowEnd.Equal(window.End) {
		return fmt.Errorf("%s reconcile returned the wrong window", stage)
	}
	if strings.TrimSpace(receipt.CommitKey) == "" || strings.TrimSpace(receipt.CommitETag) == "" || receipt.SegmentCount <= 0 {
		return fmt.Errorf("%s reconcile did not return a committed restore-verified aggregate", stage)
	}
	if receipt.DeletionAuthorized {
		return fmt.Errorf("%s reconcile violated deletion denial", stage)
	}
	return nil
}

func defaultQAMaintenanceDeps() qaMaintenanceDeps {
	return qaMaintenanceDeps{
		loadConfig: config.LoadForBootstrap,
		openDB:     sql.Open,
		tryAdvisoryLock: func(ctx context.Context, conn *sql.Conn) (bool, error) {
			var locked bool
			err := conn.QueryRowContext(
				ctx,
				"SELECT pg_try_advisory_lock($1)",
				qaMaintenanceAdvisoryLockID,
			).Scan(&locked)
			return locked, err
		},
		unlockAdvisory: func(ctx context.Context, conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", qaMaintenanceAdvisoryLockID)
			return err
		},
		planShard: defaultQAMaintenancePlanShard,
		reconcileShard: func(ctx context.Context, conn *sql.Conn, store archive.ObjectStore, window archive.Window, blobRoot, scratchRoot string) (archive.ReconcileReceipt, error) {
			reconciler := archive.NewReconciler(store, archive.NewSQLControlStore(), scratchRoot)
			reconciler.BlobRoot = blobRoot
			return reconciler.Reconcile(ctx, conn, window)
		},
		selectOldest: func(ctx context.Context, conn *sql.Conn, normal archive.Window, retentionCutoff time.Time) (archive.CatchupSelection, bool, error) {
			return archive.SelectOldestCatchup(ctx, conn, archive.NewSQLControlStore(), normal, retentionCutoff)
		},
		newObjectStore: archive.NewObjectStoreFromConfig,
		writeHeartbeat: func(ctx context.Context, db *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
			return repository.NewOpsRepository(db).UpsertJobHeartbeat(ctx, input)
		},
		now: time.Now,
	}
}

func (d qaMaintenanceDeps) withDefaults() qaMaintenanceDeps {
	defaults := defaultQAMaintenanceDeps()
	if d.loadConfig == nil {
		d.loadConfig = defaults.loadConfig
	}
	if d.openDB == nil {
		d.openDB = defaults.openDB
	}
	if d.tryAdvisoryLock == nil {
		d.tryAdvisoryLock = defaults.tryAdvisoryLock
	}
	if d.unlockAdvisory == nil {
		d.unlockAdvisory = defaults.unlockAdvisory
	}
	if d.planShard == nil {
		d.planShard = defaults.planShard
	}
	if d.reconcileShard == nil {
		d.reconcileShard = defaults.reconcileShard
	}
	if d.selectOldest == nil {
		d.selectOldest = defaults.selectOldest
	}
	if d.newObjectStore == nil {
		d.newObjectStore = defaults.newObjectStore
	}
	if d.writeHeartbeat == nil {
		d.writeHeartbeat = defaults.writeHeartbeat
	}
	if d.now == nil {
		d.now = defaults.now
	}
	return d
}

func qaMaintenanceRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--qa-maintenance-once" || arg == "--qa-maintenance-once=true" {
			return true
		}
	}
	return false
}

func runQAMaintenanceCommand(
	ctx context.Context,
	args []string,
	out io.Writer,
	deps qaMaintenanceDeps,
) (resultErr error) {
	fs := flag.NewFlagSet("qa-maintenance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var once bool
	var confirmation string
	fs.BoolVar(&once, "qa-maintenance-once", false, "run QA archive maintenance and exit")
	fs.StringVar(&confirmation, "confirm", "", "exact production QA maintenance confirmation")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("qa maintenance flags: %w", err)
	}
	if !once {
		return fmt.Errorf("qa maintenance mode was not requested")
	}
	if confirmation != qaMaintenanceConfirmation {
		return fmt.Errorf("qa maintenance confirmation mismatch")
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("qa maintenance does not accept positional arguments")
	}
	runID := strings.TrimSpace(os.Getenv("QA_MAINTENANCE_RUN_ID"))
	trigger := strings.TrimSpace(os.Getenv("QA_MAINTENANCE_TRIGGER"))
	deps = deps.withDefaults()
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load qa maintenance config: %w", err)
	}
	db, err := deps.openDB("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return fmt.Errorf("open qa maintenance database: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	startedAt := deps.now().UTC()
	sealDelayMinutes := cfg.QaArchive.SealDelayMinutes
	windowStart, windowEnd := archive.PreviousSealedHour(startedAt, sealDelayMinutes)
	normalWindow := archive.Window{Start: windowStart, End: windowEnd}
	failureLastResult := fmt.Sprintf(
		"status=failed run_id=%s trigger=%s stage=preflight error_code=maintenance_preflight_failed normal_window=%s deletion_authorized=false upload_authorized=false",
		runID, trigger, windowStart.Format(time.RFC3339),
	)

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire qa maintenance connection: %w", err)
	}
	lockAcquired := false
	connReleased := false
	heartbeatWritten := false
	defer func() {
		if !connReleased {
			if lockAcquired {
				_ = deps.unlockAdvisory(context.Background(), conn)
			}
			_ = conn.Close()
		}
		if resultErr != nil && !heartbeatWritten {
			if heartbeatErr := writeQAMaintenanceFailureHeartbeat(deps, db, startedAt, resultErr, failureLastResult); heartbeatErr != nil {
				resultErr = fmt.Errorf("%v; write failure heartbeat: %w", resultErr, heartbeatErr)
			}
		}
	}()
	if err := conn.PingContext(ctx); err != nil {
		return fmt.Errorf("ping qa maintenance database: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET lock_timeout = '100ms'"); err != nil {
		return fmt.Errorf("set qa maintenance lock timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET statement_timeout = '120s'"); err != nil {
		return fmt.Errorf("set qa maintenance statement timeout: %w", err)
	}

	locked, err := deps.tryAdvisoryLock(ctx, conn)
	if err != nil {
		return fmt.Errorf("acquire qa maintenance advisory lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("qa maintenance advisory lock already held")
	}
	lockAcquired = true
	s3Prefix := archive.ShardPrefix(windowStart)
	plan := qaMaintenancePlan{
		WindowStart: windowStart, WindowEnd: windowEnd, S3Prefix: s3Prefix,
		ArchiveEnabled: cfg.QaArchive.Enabled,
	}
	var compensationPlan *qaMaintenancePlan
	uploadAuthorized := false
	mode := qaMaintenanceReceiptMode
	if cfg.QaArchive.Enabled {
		store, err := deps.newObjectStore(ctx, cfg.QaArchive.Storage)
		if err != nil {
			return fmt.Errorf("open qa archive object store: %w", err)
		}
		cycle, cycleErr := runQAMaintenanceArchiveCycle(
			ctx,
			normalWindow,
			startedAt.Add(-qaMaintenanceSourceRetention),
			qaMaintenanceCycleDeps{
				reconcile: func(cycleCtx context.Context, window archive.Window) (archive.ReconcileReceipt, error) {
					return deps.reconcileShard(cycleCtx, conn, store, window, qaBlobRoot(), qaArchiveScratchRoot())
				},
				selectOldest: func(cycleCtx context.Context, normal archive.Window, retentionCutoff time.Time) (archive.CatchupSelection, bool, error) {
					return deps.selectOldest(cycleCtx, conn, normal, retentionCutoff)
				},
			},
		)
		if !cycle.Normal.WindowStart.IsZero() {
			plan = qaMaintenancePlanFromReceipt(cycle.Normal, true)
		}
		if cycle.CompensationSelection != nil {
			candidate := qaMaintenancePlan{
				WindowStart:    cycle.CompensationSelection.Window.Start,
				WindowEnd:      cycle.CompensationSelection.Window.End,
				S3Prefix:       archive.ShardPrefix(cycle.CompensationSelection.Window.Start),
				ArchiveEnabled: true,
			}
			if cycle.Compensation != nil {
				candidate = qaMaintenancePlanFromReceipt(*cycle.Compensation, true)
			} else if cycle.CompensationSelection.Disposition == archive.CatchupDispositionSourceUnavailableAfterRetention {
				candidate.State = archive.StateFailed
				candidate.VerificationErrorCode = archive.IntegritySourceUnavailableAfterRetention
			} else {
				candidate.State = archive.StateFailed
				candidate.VerificationErrorCode = cycle.FailureCode
			}
			compensationPlan = &candidate
		}
		if cycleErr != nil {
			if plan.State == "" {
				plan.State = archive.StateFailed
				plan.VerificationErrorCode = cycle.FailureCode
			}
			failureLastResult = qaMaintenanceCycleLastResult("failed", runID, trigger, plan, compensationPlan, cycle.FailureStage, cycle.FailureCode)
			return cycleErr
		}
		uploadAuthorized = true
		mode = qaMaintenanceReceiptModeUpload
	} else {
		plan, err = deps.planShard(ctx, conn, windowStart, windowEnd, s3Prefix, false)
		if err != nil {
			return fmt.Errorf("plan qa archive shard: %w", err)
		}
		plan.State = archive.StatePending
	}

	if err := deps.unlockAdvisory(ctx, conn); err != nil {
		failureLastResult = qaMaintenanceCycleLastResult("failed", runID, trigger, plan, compensationPlan, "advisory_unlock", "advisory_unlock_failed")
		return fmt.Errorf("release qa maintenance advisory lock: %w", err)
	}
	lockAcquired = false
	if err := conn.Close(); err != nil {
		failureLastResult = qaMaintenanceCycleLastResult("failed", runID, trigger, plan, compensationPlan, "connection_release", "connection_release_failed")
		return fmt.Errorf("release qa maintenance connection: %w", err)
	}
	connReleased = true

	completedAt := deps.now().UTC()
	duration := completedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	durationMs := duration.Milliseconds()
	status := "committed"
	if !plan.ArchiveEnabled {
		status = "planned"
	}
	lastResult := qaMaintenanceCycleLastResult(status, runID, trigger, plan, compensationPlan, "", "")
	failureLastResult = qaMaintenanceCycleLastResult("failed", runID, trigger, plan, compensationPlan, "heartbeat_write", "heartbeat_write_failed")
	heartbeatCtx, cancelHeartbeat := context.WithTimeout(context.Background(), qaMaintenanceHeartbeatTimeout)
	defer cancelHeartbeat()
	if err := deps.writeHeartbeat(heartbeatCtx, db, &service.OpsUpsertJobHeartbeatInput{
		JobName:        qaMaintenanceJobName,
		LastRunAt:      &startedAt,
		LastSuccessAt:  &completedAt,
		LastDurationMs: &durationMs,
		LastResult:     &lastResult,
	}); err != nil {
		return fmt.Errorf("write qa maintenance heartbeat: %w", err)
	}
	heartbeatWritten = true

	receipt := struct {
		ReceiptVersion     int                `json:"receipt_version"`
		Mode               string             `json:"mode"`
		OK                 bool               `json:"ok"`
		JobName            string             `json:"job_name"`
		RunID              string             `json:"run_id"`
		Trigger            string             `json:"trigger"`
		CompletedAt        time.Time          `json:"completed_at"`
		Plan               qaMaintenancePlan  `json:"plan"`
		Compensation       *qaMaintenancePlan `json:"compensation,omitempty"`
		DeletionAuthorized bool               `json:"deletion_authorized"`
		UploadAuthorized   bool               `json:"upload_authorized"`
	}{
		ReceiptVersion:     qaMaintenanceReceiptVersion,
		Mode:               mode,
		OK:                 true,
		JobName:            qaMaintenanceJobName,
		RunID:              runID,
		Trigger:            trigger,
		CompletedAt:        completedAt,
		Plan:               plan,
		Compensation:       compensationPlan,
		DeletionAuthorized: false,
		UploadAuthorized:   uploadAuthorized,
	}
	if err := json.NewEncoder(out).Encode(receipt); err != nil {
		return fmt.Errorf("encode qa maintenance receipt: %w", err)
	}
	return nil
}

func defaultQAMaintenancePlanShard(
	ctx context.Context,
	conn *sql.Conn,
	windowStart, windowEnd time.Time,
	s3Prefix string,
	archiveEnabled bool,
) (qaMaintenancePlan, error) {
	windowStart = windowStart.UTC()
	windowEnd = windowEnd.UTC()
	shardID, err := archive.NewSQLControlStore().EnsureShard(
		ctx,
		conn,
		archive.Window{Start: windowStart, End: windowEnd},
	)
	if err != nil {
		return qaMaintenancePlan{}, fmt.Errorf("ensure qa archive shard before source inspection: %w", err)
	}
	var recordCount int64
	var blobRefCount int64
	if err := conn.QueryRowContext(
		ctx,
		`SELECT COUNT(*),
		        COUNT(*) FILTER (
		            WHERE COALESCE(NULLIF(blob_uri, ''), NULLIF(request_blob_uri, ''), NULLIF(response_blob_uri, ''), NULLIF(stream_blob_uri, '')) IS NOT NULL
		        )
		   FROM qa_records
		  WHERE created_at >= $1 AND created_at < $2`,
		windowStart,
		windowEnd,
	).Scan(&recordCount, &blobRefCount); err != nil {
		return qaMaintenancePlan{}, fmt.Errorf("count qa_records for shard window: %w", err)
	}

	result, err := conn.ExecContext(
		ctx,
		`UPDATE qa_archive_shards SET
		    record_count=$1,
		    blob_ref_count=$2,
		    cleanup_eligible=false,
		    updated_at=now()
		  WHERE id=$3 AND state IN ('pending', 'failed')`,
		recordCount,
		blobRefCount,
		shardID,
	)
	if err != nil {
		return qaMaintenancePlan{}, fmt.Errorf("update qa archive shard source counts: %w", err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil {
		return qaMaintenancePlan{}, fmt.Errorf("inspect qa archive shard source count update: %w", rowsErr)
	} else if changed > 1 {
		return qaMaintenancePlan{}, fmt.Errorf("qa archive shard source count update changed %d rows", changed)
	}

	return qaMaintenancePlan{
		WindowStart:    windowStart,
		WindowEnd:      windowEnd,
		S3Prefix:       s3Prefix,
		RecordCount:    recordCount,
		BlobRefCount:   blobRefCount,
		ArchiveEnabled: archiveEnabled,
	}, nil
}

func qaMaintenancePlanFromReceipt(receipt archive.ReconcileReceipt, archiveEnabled bool) qaMaintenancePlan {
	return qaMaintenancePlan{
		WindowStart:          receipt.WindowStart,
		WindowEnd:            receipt.WindowEnd,
		S3Prefix:             archive.ShardPrefix(receipt.WindowStart),
		RecordCount:          receipt.RecordCount,
		BlobRefCount:         receipt.BlobRefCount,
		AggregateRecordCount: receipt.RecordCount,
		ArchiveEnabled:       archiveEnabled,
		Uploaded:             receipt.Uploaded,
		SegmentCount:         receipt.SegmentCount,
		CommitKey:            receipt.CommitKey,
		CommitETag:           receipt.CommitETag,
		BlobPresentCount:     receipt.BlobPresentCount,
		BlobMissingCount:     receipt.BlobMissingCount,
		RestoreVerified:      true,
		State:                archive.StateCommitted,
	}
}

func qaMaintenanceCycleLastResult(status, runID, trigger string, normal qaMaintenancePlan, compensation *qaMaintenancePlan, failureStage, failureCode string) string {
	parts := []string{
		"status=" + status,
		"run_id=" + runID,
		"trigger=" + trigger,
	}
	if failureStage != "" {
		parts = append(parts, "stage="+failureStage)
	}
	if failureCode != "" {
		parts = append(parts, "error_code="+failureCode)
	}
	parts = append(parts,
		"normal_window="+normal.WindowStart.Format(time.RFC3339),
		"normal_state="+normal.State,
		"normal_commit_etag="+normal.CommitETag,
		fmt.Sprintf("normal_segment_count=%d", normal.SegmentCount),
		fmt.Sprintf("normal_restore_verified=%t", normal.RestoreVerified),
		fmt.Sprintf("normal_aggregate_record_count=%d", normal.AggregateRecordCount),
		fmt.Sprintf("normal_aggregate_blob_ref_count=%d", normal.BlobRefCount),
		fmt.Sprintf("normal_blob_present_count=%d", normal.BlobPresentCount),
		fmt.Sprintf("normal_blob_missing_count=%d", normal.BlobMissingCount),
		"normal_verification_error_code="+normal.VerificationErrorCode,
	)
	if compensation != nil {
		parts = append(parts,
			"compensation_window="+compensation.WindowStart.Format(time.RFC3339),
			"compensation_state="+compensation.State,
			"compensation_commit_etag="+compensation.CommitETag,
			fmt.Sprintf("compensation_segment_count=%d", compensation.SegmentCount),
			fmt.Sprintf("compensation_restore_verified=%t", compensation.RestoreVerified),
			fmt.Sprintf("compensation_aggregate_record_count=%d", compensation.AggregateRecordCount),
			fmt.Sprintf("compensation_aggregate_blob_ref_count=%d", compensation.BlobRefCount),
			fmt.Sprintf("compensation_blob_present_count=%d", compensation.BlobPresentCount),
			fmt.Sprintf("compensation_blob_missing_count=%d", compensation.BlobMissingCount),
			"compensation_error_code="+compensation.VerificationErrorCode,
		)
	}
	parts = append(parts,
		"cleanup_eligible=false",
		"deletion_authorized=false",
		"upload_authorized="+fmt.Sprintf("%t", normal.ArchiveEnabled),
	)
	return strings.Join(parts, " ")
}

func writeQAMaintenanceFailureHeartbeat(
	deps qaMaintenanceDeps,
	db *sql.DB,
	startedAt time.Time,
	failure error,
	lastResult string,
) error {
	failedAt := deps.now().UTC()
	duration := failedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	durationMs := duration.Milliseconds()
	message := truncateErr(failure)
	if strings.TrimSpace(lastResult) == "" {
		lastResult = "status=failed deletion_authorized=false upload_authorized=false"
	}
	heartbeatCtx, cancel := context.WithTimeout(context.Background(), qaMaintenanceHeartbeatTimeout)
	defer cancel()
	return deps.writeHeartbeat(heartbeatCtx, db, &service.OpsUpsertJobHeartbeatInput{
		JobName: qaMaintenanceJobName, LastRunAt: &startedAt,
		LastErrorAt: &failedAt, LastError: &message,
		LastDurationMs: &durationMs, LastResult: &lastResult,
	})
}

func qaBlobRoot() string {
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "/app/data"
	}
	return filepath.Join(dataDir, "qa_blobs")
}

func qaArchiveScratchRoot() string {
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "/app/data"
	}
	return filepath.Join(dataDir, "qa_archive_tmp")
}

func truncateErr(err error) string {
	msg := err.Error()
	if len(msg) > 500 {
		return msg[:500]
	}
	return msg
}

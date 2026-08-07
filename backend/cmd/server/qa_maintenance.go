package main

import (
	"context"
	"database/sql"
	"encoding/json"
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
	qaMaintenanceBackfillTimeout   = 30 * time.Minute
)

type qaMaintenanceDeps struct {
	loadConfig      func() (*config.Config, error)
	openDB          func(driverName, dataSourceName string) (*sql.DB, error)
	tryAdvisoryLock func(context.Context, *sql.Conn) (bool, error)
	unlockAdvisory  func(context.Context, *sql.Conn) error
	planShard       func(context.Context, *sql.Conn, time.Time, time.Time, string, bool, int) (qaMaintenancePlan, error)
	reconcileShard  func(context.Context, *sql.Conn, archive.ObjectStore, archive.Window, string, string) (archive.ReconcileReceipt, error)
	newObjectStore  func(context.Context, config.QACaptureStorageConfig) (archive.ObjectStore, error)
	writeHeartbeat  func(context.Context, *sql.DB, *service.OpsUpsertJobHeartbeatInput) error
	now             func() time.Time
}

type qaMaintenancePlan struct {
	WindowStart    time.Time `json:"window_start"`
	WindowEnd      time.Time `json:"window_end"`
	S3Prefix       string    `json:"s3_prefix"`
	RecordCount    int64     `json:"record_count"`
	BlobRefCount   int64     `json:"blob_ref_count"`
	ArchiveEnabled bool      `json:"archive_enabled"`
	Uploaded       bool      `json:"uploaded"`
	SegmentID      string    `json:"segment_id,omitempty"`
	SegmentCount   int       `json:"segment_count,omitempty"`
	CommitKey      string    `json:"commit_key,omitempty"`
	CommitETag     string    `json:"commit_etag,omitempty"`
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
	var backfillOnce bool
	var confirmation string
	fs.BoolVar(&once, "qa-maintenance-once", false, "run QA archive maintenance and exit")
	fs.BoolVar(&backfillOnce, "qa-maintenance-backfill-once", false, "archive the oldest uncommitted hour")
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
			if heartbeatErr := writeQAMaintenanceFailureHeartbeat(deps, db, startedAt, resultErr); heartbeatErr != nil {
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
	statementTimeout := "120s"
	if backfillOnce {
		statementTimeout = fmt.Sprintf("%ds", int(qaMaintenanceBackfillTimeout.Seconds()))
	}
	if _, err := conn.ExecContext(ctx, "SET statement_timeout = '"+statementTimeout+"'"); err != nil {
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
	sealDelayMinutes := cfg.QaArchive.SealDelayMinutes
	windowStart, windowEnd, err := resolveMaintenanceWindow(ctx, conn, startedAt, sealDelayMinutes, backfillOnce)
	if err != nil {
		return fmt.Errorf("resolve qa archive window: %w", err)
	}
	s3Prefix := archive.ShardPrefix(windowStart)
	plan := qaMaintenancePlan{
		WindowStart: windowStart, WindowEnd: windowEnd, S3Prefix: s3Prefix,
		ArchiveEnabled: cfg.QaArchive.Enabled,
	}
	uploadAuthorized := false
	mode := qaMaintenanceReceiptMode
	if cfg.QaArchive.Enabled {
		store, err := deps.newObjectStore(ctx, cfg.QaArchive.Storage)
		if err != nil {
			return fmt.Errorf("open qa archive object store: %w", err)
		}
		reconciled, err := deps.reconcileShard(
			ctx, conn, store, archive.Window{Start: windowStart, End: windowEnd},
			qaBlobRoot(), qaArchiveScratchRoot(),
		)
		if err != nil {
			return fmt.Errorf("reconcile qa archive shard: %w", err)
		}
		plan.Uploaded = reconciled.Uploaded
		plan.SegmentCount = reconciled.SegmentCount
		plan.CommitKey = reconciled.CommitKey
		plan.CommitETag = reconciled.CommitETag
		plan.RecordCount = reconciled.RecordCount
		plan.BlobRefCount = reconciled.BlobRefCount
		uploadAuthorized = true
		mode = qaMaintenanceReceiptModeUpload
	} else {
		plan, err = deps.planShard(ctx, conn, windowStart, windowEnd, s3Prefix, false, sealDelayMinutes)
		if err != nil {
			return fmt.Errorf("plan qa archive shard: %w", err)
		}
	}

	if err := deps.unlockAdvisory(ctx, conn); err != nil {
		return fmt.Errorf("release qa maintenance advisory lock: %w", err)
	}
	lockAcquired = false
	if err := conn.Close(); err != nil {
		return fmt.Errorf("release qa maintenance connection: %w", err)
	}
	connReleased = true

	completedAt := deps.now().UTC()
	duration := completedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	durationMs := duration.Milliseconds()
	lastResult := fmt.Sprintf(
		"archive_enabled=%t uploaded=%t window=%s records=%d segments=%d commit_etag=%s deletion_authorized=false upload_authorized=%t",
		plan.ArchiveEnabled,
		plan.Uploaded,
		plan.WindowStart.Format(time.RFC3339),
		plan.RecordCount,
		plan.SegmentCount,
		plan.CommitETag,
		uploadAuthorized,
	)
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
		ReceiptVersion     int               `json:"receipt_version"`
		Mode               string            `json:"mode"`
		OK                 bool              `json:"ok"`
		JobName            string            `json:"job_name"`
		CompletedAt        time.Time         `json:"completed_at"`
		Plan               qaMaintenancePlan `json:"plan"`
		DeletionAuthorized bool              `json:"deletion_authorized"`
		UploadAuthorized   bool              `json:"upload_authorized"`
		BackfillOnce       bool              `json:"backfill_once"`
	}{
		ReceiptVersion:     qaMaintenanceReceiptVersion,
		Mode:               mode,
		OK:                 true,
		JobName:            qaMaintenanceJobName,
		CompletedAt:        completedAt,
		Plan:               plan,
		DeletionAuthorized: false,
		UploadAuthorized:   uploadAuthorized,
		BackfillOnce:       backfillOnce,
	}
	if err := json.NewEncoder(out).Encode(receipt); err != nil {
		return fmt.Errorf("encode qa maintenance receipt: %w", err)
	}
	return nil
}

func resolveMaintenanceWindow(
	ctx context.Context,
	conn *sql.Conn,
	runAt time.Time,
	sealDelayMinutes int,
	backfillOnce bool,
) (time.Time, time.Time, error) {
	if backfillOnce {
		sealedBefore := runAt.UTC().Add(-time.Duration(max(sealDelayMinutes, 0)) * time.Minute)
		return findOldestUncommittedHour(ctx, conn, sealedBefore)
	}
	start, end := archive.PreviousSealedHour(runAt, sealDelayMinutes)
	return start, end, nil
}

func findOldestUncommittedHour(ctx context.Context, conn *sql.Conn, sealedBefore time.Time) (time.Time, time.Time, error) {
	var windowStart time.Time
	err := conn.QueryRowContext(ctx, `
		SELECT h.window_start
		  FROM (
		    SELECT date_trunc('hour', created_at AT TIME ZONE 'UTC') AS window_start,
		           COUNT(*)::bigint AS record_count
		      FROM qa_records
		     GROUP BY 1
		  ) h
		 WHERE h.window_start + interval '1 hour' <= $2
		   AND NOT EXISTS (
		    SELECT 1 FROM qa_archive_shards s
		     WHERE s.window_start = h.window_start
		       AND s.generation = 0
		       AND s.state = $1
		       AND s.aggregate_record_count = h.record_count
		       AND s.aggregate_blob_missing_count = 0
		 )
		 ORDER BY h.window_start
		 LIMIT 1`,
		archive.StateCommitted, sealedBefore.UTC(),
	).Scan(&windowStart)
	if err == sql.ErrNoRows {
		return time.Time{}, time.Time{}, fmt.Errorf("no qa archive backfill window remaining")
	}
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	windowStart = windowStart.UTC()
	return windowStart, windowStart.Add(time.Hour), nil
}

func defaultQAMaintenancePlanShard(
	ctx context.Context,
	conn *sql.Conn,
	windowStart, windowEnd time.Time,
	s3Prefix string,
	archiveEnabled bool,
	_ int,
) (qaMaintenancePlan, error) {
	windowStart = windowStart.UTC()
	windowEnd = windowEnd.UTC()
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

	now := time.Now().UTC()
	if _, err := conn.ExecContext(
		ctx,
		`INSERT INTO qa_archive_shards (
		    window_start, window_end, generation, state, record_count, blob_ref_count,
		    s3_prefix, first_attempt_at, updated_at
		) VALUES ($1, $2, 0, $3, $4, $5, $6, $7, $7)
		ON CONFLICT (window_start, generation) DO UPDATE SET
		    record_count = EXCLUDED.record_count,
		    blob_ref_count = EXCLUDED.blob_ref_count,
		    s3_prefix = EXCLUDED.s3_prefix,
		    updated_at = EXCLUDED.updated_at
		  WHERE qa_archive_shards.state IN ('pending', 'failed')`,
		windowStart,
		windowEnd,
		archive.StatePending,
		recordCount,
		blobRefCount,
		s3Prefix,
		now,
	); err != nil {
		return qaMaintenancePlan{}, fmt.Errorf("upsert qa_archive_shards control row: %w", err)
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

func writeQAMaintenanceFailureHeartbeat(
	deps qaMaintenanceDeps,
	db *sql.DB,
	startedAt time.Time,
	failure error,
) error {
	failedAt := deps.now().UTC()
	duration := failedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	durationMs := duration.Milliseconds()
	message := truncateErr(failure)
	lastResult := "status=failed deletion_authorized=false upload_authorized=false"
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

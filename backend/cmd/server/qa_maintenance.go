package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	qaMaintenanceConfirmation     = "tokenkey-prod-qa-maintenance-v1"
	qaMaintenanceReceiptVersion   = 1
	qaMaintenanceReceiptMode      = "qa_maintenance_archive_only"
	qaMaintenanceJobName          = "qa-maintenance"
	qaMaintenanceAdvisoryLockID   = int64(0x51414D41) // 'QAMA'
	qaMaintenanceHeartbeatTimeout = 5 * time.Second
)

type qaMaintenanceDeps struct {
	loadConfig      func() (*config.Config, error)
	openDB          func(driverName, dataSourceName string) (*sql.DB, error)
	tryAdvisoryLock func(context.Context, *sql.Conn) (bool, error)
	unlockAdvisory  func(context.Context, *sql.Conn) error
	planShard       func(context.Context, *sql.Conn, time.Time, string, bool, int) (qaMaintenancePlan, error)
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
) error {
	fs := flag.NewFlagSet("qa-maintenance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var once bool
	var confirmation string
	fs.BoolVar(&once, "qa-maintenance-once", false, "run QA archive-only maintenance and exit")
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

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire qa maintenance connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
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
	defer func() { _ = deps.unlockAdvisory(ctx, conn) }()

	startedAt := deps.now().UTC()
	sealDelayMinutes := cfg.QaArchive.SealDelayMinutes
	windowStart, _ := archive.PreviousSealedHour(startedAt, sealDelayMinutes)
	s3Prefix := archive.ShardPrefix(windowStart)
	plan, err := deps.planShard(ctx, conn, startedAt, s3Prefix, cfg.QaArchive.Enabled, sealDelayMinutes)
	if err != nil {
		return fmt.Errorf("plan qa archive shard: %w", err)
	}

	completedAt := deps.now().UTC()
	duration := completedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	durationMs := duration.Milliseconds()
	lastResult := fmt.Sprintf(
		"archive_enabled=%t window=%s records=%d deletion_authorized=false upload_authorized=false",
		plan.ArchiveEnabled,
		plan.WindowStart.Format(time.RFC3339),
		plan.RecordCount,
	)
	heartbeatCtx, cancelHeartbeat := context.WithTimeout(ctx, qaMaintenanceHeartbeatTimeout)
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

	receipt := struct {
		ReceiptVersion     int               `json:"receipt_version"`
		Mode               string            `json:"mode"`
		OK                 bool              `json:"ok"`
		JobName            string            `json:"job_name"`
		CompletedAt        time.Time         `json:"completed_at"`
		Plan               qaMaintenancePlan `json:"plan"`
		DeletionAuthorized bool              `json:"deletion_authorized"`
		UploadAuthorized   bool              `json:"upload_authorized"`
	}{
		ReceiptVersion:     qaMaintenanceReceiptVersion,
		Mode:               qaMaintenanceReceiptMode,
		OK:                 true,
		JobName:            qaMaintenanceJobName,
		CompletedAt:        completedAt,
		Plan:               plan,
		DeletionAuthorized: false,
		UploadAuthorized:   false,
	}
	if err := json.NewEncoder(out).Encode(receipt); err != nil {
		return fmt.Errorf("encode qa maintenance receipt: %w", err)
	}
	return nil
}

func defaultQAMaintenancePlanShard(
	ctx context.Context,
	conn *sql.Conn,
	runAt time.Time,
	s3Prefix string,
	archiveEnabled bool,
	sealDelayMinutes int,
) (qaMaintenancePlan, error) {
	windowStart, windowEnd := archive.PreviousSealedHour(runAt, sealDelayMinutes)
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

	now := runAt.UTC()
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

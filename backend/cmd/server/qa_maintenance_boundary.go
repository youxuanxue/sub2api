package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/lifecycle"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	qaBoundaryConfirmation     = "tokenkey-prod-qa-boundary-v1"
	qaBoundaryReceiptVersion   = 1
	qaBoundaryReceiptMode      = "qa_maintenance_boundary"
	qaBoundaryJobName          = "qa-boundary"
	qaBoundaryHeartbeatTimeout = 5 * time.Second
)

type qaBoundaryDeps struct {
	loadConfig      func() (*config.Config, error)
	openDB          func(driverName, dataSourceName string) (*sql.DB, error)
	tryAdvisoryLock func(context.Context, *sql.Conn) (bool, error)
	unlockAdvisory  func(context.Context, *sql.Conn) error
	runBoundary     func(context.Context, *sql.DB, lifecycle.ControlStore, lifecycle.Options) (lifecycle.BoundaryResult, error)
	writeHeartbeat  func(context.Context, *sql.DB, *service.OpsUpsertJobHeartbeatInput) error
	now             func() time.Time
}

func defaultQABoundaryDeps() qaBoundaryDeps {
	return qaBoundaryDeps{
		loadConfig: config.LoadForBootstrap,
		openDB:     sql.Open,
		tryAdvisoryLock: func(ctx context.Context, conn *sql.Conn) (bool, error) {
			var locked bool
			err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", archive.MaintenanceAdvisoryLockID).Scan(&locked)
			return locked, err
		},
		unlockAdvisory: func(ctx context.Context, conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", archive.MaintenanceAdvisoryLockID)
			return err
		},
		runBoundary: func(ctx context.Context, db *sql.DB, control lifecycle.ControlStore, opts lifecycle.Options) (lifecycle.BoundaryResult, error) {
			return lifecycle.RunBoundary(ctx, db, control, opts)
		},
		writeHeartbeat: func(ctx context.Context, db *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
			return repository.NewOpsRepository(db).UpsertJobHeartbeat(ctx, input)
		},
		now: time.Now,
	}
}

func (d qaBoundaryDeps) withDefaults() qaBoundaryDeps {
	defaults := defaultQABoundaryDeps()
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
	if d.runBoundary == nil {
		d.runBoundary = defaults.runBoundary
	}
	if d.writeHeartbeat == nil {
		d.writeHeartbeat = defaults.writeHeartbeat
	}
	if d.now == nil {
		d.now = defaults.now
	}
	return d
}

func qaBoundaryRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--qa-boundary-once" || arg == "--qa-boundary-once=true" ||
			arg == "--qa-cutover-inventory" || arg == "--qa-cutover-inventory=true" {
			return true
		}
	}
	return false
}

func runQACutoverInventory(ctx context.Context, out io.Writer, deps qaBoundaryDeps) error {
	deps = deps.withDefaults()
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load qa cutover inventory config: %w", err)
	}
	db, err := deps.openDB("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return fmt.Errorf("open qa cutover inventory database: %w", err)
	}
	defer func() { _ = db.Close() }()
	inv, err := lifecycle.BuildCutoverInventory(ctx, db, lifecycle.HourlyHorizon)
	if err != nil {
		return err
	}
	payload, err := lifecycle.EncodeCutoverInventory(inv)
	if err != nil {
		return err
	}
	if _, err := out.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write qa cutover inventory: %w", err)
	}
	return nil
}

func runQABoundaryCommand(ctx context.Context, args []string, out io.Writer, deps qaBoundaryDeps) error {
	if len(args) > 0 && (args[0] == "--qa-cutover-inventory" || args[0] == "--qa-cutover-inventory=true") {
		return runQACutoverInventory(ctx, out, deps)
	}
	fs := flag.NewFlagSet("qa-boundary", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var once bool
	var confirmation string
	fs.BoolVar(&once, "qa-boundary-once", false, "run QA lifecycle boundary maintenance and exit")
	fs.StringVar(&confirmation, "confirm", "", "exact production QA boundary confirmation")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("qa boundary flags: %w", err)
	}
	if !once {
		return fmt.Errorf("qa boundary mode was not requested")
	}
	if confirmation != qaBoundaryConfirmation {
		return fmt.Errorf("qa boundary confirmation mismatch")
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("qa boundary does not accept positional arguments")
	}

	deps = deps.withDefaults()
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load qa boundary config: %w", err)
	}
	db, err := deps.openDB("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return fmt.Errorf("open qa boundary database: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)

	startedAt := deps.now().UTC()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire qa boundary connection: %w", err)
	}
	lockAcquired := false
	defer func() {
		if lockAcquired {
			_ = deps.unlockAdvisory(context.Background(), conn)
		}
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, "SET lock_timeout = '100ms'"); err != nil {
		return fmt.Errorf("set qa boundary lock timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET statement_timeout = '120s'"); err != nil {
		return fmt.Errorf("set qa boundary statement timeout: %w", err)
	}
	locked, err := deps.tryAdvisoryLock(ctx, conn)
	if err != nil {
		return fmt.Errorf("acquire qa boundary advisory lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("qa boundary advisory lock already held")
	}
	lockAcquired = true

	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "/app/data"
	}
	result, err := deps.runBoundary(ctx, db, lifecycle.NewSQLControlAdapter(archive.NewSQLControlStore()), lifecycle.Options{
		HoursAhead: lifecycle.HourlyHorizon,
		BlobRoot:   dataDir,
		DLQRoot:    dataDir,
	})
	if lockAcquired {
		if unlockErr := deps.unlockAdvisory(ctx, conn); unlockErr != nil {
			return fmt.Errorf("release qa boundary advisory lock: %w", unlockErr)
		}
		lockAcquired = false
	}
	if closeErr := conn.Close(); closeErr != nil {
		return fmt.Errorf("close qa boundary connection: %w", closeErr)
	}
	completedAt := deps.now().UTC()
	durationMs := completedAt.Sub(startedAt).Milliseconds()
	status := "ok"
	if err != nil {
		status = "failed"
	}
	lastResult := fmt.Sprintf(
		"status=%s phase=boundary provision_covered=%d/%d deletion_authorized=true",
		status,
		result.Provision.RangesCovered,
		result.Provision.RangesRequired,
	)
	if result.Expiry != nil && result.Expiry.PartitionName != "" {
		lastResult += fmt.Sprintf(" dropped=%s terminal_gap=%t", result.Expiry.PartitionName, result.Expiry.TerminalGap)
	}
	heartbeatCtx, cancel := context.WithTimeout(ctx, qaBoundaryHeartbeatTimeout)
	defer cancel()
	_ = deps.writeHeartbeat(heartbeatCtx, db, &service.OpsUpsertJobHeartbeatInput{
		JobName:        qaBoundaryJobName,
		LastRunAt:      &startedAt,
		LastDurationMs: &durationMs,
		LastResult:     &lastResult,
	})
	receipt := struct {
		ReceiptVersion     int                      `json:"receipt_version"`
		Mode               string                   `json:"mode"`
		OK                 bool                     `json:"ok"`
		JobName            string                   `json:"job_name"`
		RunID              string                   `json:"run_id"`
		Trigger            string                   `json:"trigger"`
		CompletedAt        time.Time                `json:"completed_at"`
		Boundary           lifecycle.BoundaryResult `json:"boundary"`
		DeletionAuthorized bool                     `json:"deletion_authorized"`
	}{
		ReceiptVersion:     qaBoundaryReceiptVersion,
		Mode:               qaBoundaryReceiptMode,
		OK:                 err == nil,
		JobName:            qaBoundaryJobName,
		RunID:              strings.TrimSpace(os.Getenv("QA_MAINTENANCE_RUN_ID")),
		Trigger:            strings.TrimSpace(os.Getenv("QA_MAINTENANCE_TRIGGER")),
		CompletedAt:        completedAt,
		Boundary:           result,
		DeletionAuthorized: true,
	}
	if encErr := json.NewEncoder(out).Encode(receipt); encErr != nil {
		return fmt.Errorf("encode qa boundary receipt: %w", encErr)
	}
	if err != nil {
		return err
	}
	return nil
}

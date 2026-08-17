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
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/lifecycle"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	qaBoundaryConfirmation      = "tokenkey-prod-qa-boundary-v1"
	qaBoundaryReceiptVersion    = 1
	qaBoundaryReceiptMode       = "qa_maintenance_boundary"
	qaBoundaryJobName           = "qa-boundary"
	qaBoundaryHeartbeatTimeout  = 5 * time.Second
	qaBoundaryConnectionOptions = "-c lock_timeout=100ms -c statement_timeout=120s"
)

type qaBoundaryDeps struct {
	loadConfig            func() (*config.Config, error)
	openDB                func(driverName, dataSourceName string) (*sql.DB, error)
	tryAdvisoryLock       func(context.Context, *sql.Conn) (bool, error)
	unlockAdvisory        func(context.Context, *sql.Conn) error
	singleOwnerActive     func(context.Context, *sql.DB) (bool, error)
	runTransitionBoundary func(context.Context, *sql.DB, lifecycle.TransitionControlStore, lifecycle.Options) (lifecycle.TransitionBoundaryResult, error)
	writeHeartbeat        func(context.Context, *sql.DB, *service.OpsUpsertJobHeartbeatInput) error
	now                   func() time.Time
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
		singleOwnerActive: func(ctx context.Context, db *sql.DB) (bool, error) {
			var active bool
			err := db.QueryRowContext(ctx, `SELECT EXISTS (
				SELECT 1 FROM qa_lifecycle_receipts WHERE phase = 'single_owner_activate'
			)`).Scan(&active)
			return active, err
		},
		runTransitionBoundary: lifecycle.RunTransitionBoundary,
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
	if d.singleOwnerActive == nil {
		d.singleOwnerActive = defaults.singleOwnerActive
	}
	if d.runTransitionBoundary == nil {
		d.runTransitionBoundary = defaults.runTransitionBoundary
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
		if arg == "--qa-boundary-once" {
			return true
		}
		if strings.HasPrefix(arg, "--qa-boundary-once=") {
			return true
		}
	}
	return false
}

func ensureQABoundaryPreActivation(ctx context.Context, db *sql.DB, deps qaBoundaryDeps) error {
	active, err := deps.singleOwnerActive(ctx, db)
	if err != nil {
		return fmt.Errorf("read QA single-owner activation: %w", err)
	}
	if active {
		return errors.New("qa boundary is retired after single-owner activation")
	}
	return nil
}

func openQABoundaryDB(deps qaBoundaryDeps) (*sql.DB, error) {
	deps = deps.withDefaults()
	cfg, err := deps.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("load qa boundary config: %w", err)
	}
	dsn := cfg.Database.DSNWithTimezone(cfg.Timezone) + " options='" + qaBoundaryConnectionOptions + "'"
	db, err := deps.openDB("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open qa boundary database: %w", err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	return db, nil
}

func withQAMaintenanceLock(ctx context.Context, db *sql.DB, deps qaBoundaryDeps, fn func(context.Context, *sql.DB) error) error {
	deps = deps.withDefaults()
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
	if err := fn(ctx, db); err != nil {
		return err
	}
	if unlockErr := deps.unlockAdvisory(ctx, conn); unlockErr != nil {
		return fmt.Errorf("release qa boundary advisory lock: %w", unlockErr)
	}
	lockAcquired = false
	return nil
}

func runQABoundaryCommand(ctx context.Context, args []string, out io.Writer, deps qaBoundaryDeps) error {
	fs := flag.NewFlagSet("qa-boundary", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var once bool
	var confirmation string
	fs.BoolVar(&once, "qa-boundary-once", false, "run QA lifecycle boundary maintenance and exit")
	fs.StringVar(&confirmation, "confirm", "", "exact production QA confirmation")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("qa boundary flags: %w", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("qa boundary does not accept positional arguments")
	}
	if !once {
		return fmt.Errorf("qa boundary mode was not requested")
	}
	if confirmation != qaBoundaryConfirmation {
		return fmt.Errorf("qa boundary confirmation mismatch")
	}
	deps = deps.withDefaults()
	db, err := openQABoundaryDB(deps)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	startedAt := deps.now().UTC()
	var result lifecycle.TransitionBoundaryResult
	var runErr error
	lockErr := withQAMaintenanceLock(ctx, db, deps, func(ctx context.Context, lockedDB *sql.DB) error {
		if err := ensureQABoundaryPreActivation(ctx, lockedDB, deps); err != nil {
			return err
		}
		dataDir := qaDataRoot()
		result, runErr = deps.runTransitionBoundary(
			ctx,
			lockedDB,
			lifecycle.NewSQLControlAdapter(archive.NewSQLControlStore()),
			lifecycle.Options{HoursAhead: lifecycle.HourlyHorizon, BlobRoot: dataDir, DLQRoot: dataDir},
		)
		return runErr
	})
	if lockErr != nil {
		runErr = lockErr
	}

	completedAt := deps.now().UTC()
	duration := completedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	durationMs := duration.Milliseconds()
	runID := strings.TrimSpace(os.Getenv("QA_MAINTENANCE_RUN_ID"))
	trigger := strings.TrimSpace(os.Getenv("QA_MAINTENANCE_TRIGGER"))
	status := "ok"
	if runErr != nil {
		status = "failed"
	}
	lastResult := fmt.Sprintf(
		"status=%s phase=boundary run_id=%s trigger=%s provision_covered=%d/%d provision_attempts=%d provision_lock_retries=%d deletion_authorized=%t",
		status,
		runID,
		trigger,
		result.Provision.RangesCovered,
		result.Provision.RangesRequired,
		result.Provision.Attempts,
		result.Provision.LockRetries,
		result.DeletionAuthorized,
	)
	heartbeat := &service.OpsUpsertJobHeartbeatInput{
		JobName:        qaBoundaryJobName,
		LastRunAt:      &startedAt,
		LastDurationMs: &durationMs,
		LastResult:     &lastResult,
	}
	if runErr == nil {
		heartbeat.LastSuccessAt = &completedAt
	} else {
		message := truncateErr(runErr)
		heartbeat.LastErrorAt = &completedAt
		heartbeat.LastError = &message
	}
	heartbeatCtx, cancel := context.WithTimeout(context.Background(), qaBoundaryHeartbeatTimeout)
	defer cancel()
	if heartbeatErr := deps.writeHeartbeat(heartbeatCtx, db, heartbeat); heartbeatErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("write qa boundary heartbeat: %w", heartbeatErr))
	}
	receipt := struct {
		ReceiptVersion     int                                `json:"receipt_version"`
		Mode               string                             `json:"mode"`
		OK                 bool                               `json:"ok"`
		JobName            string                             `json:"job_name"`
		RunID              string                             `json:"run_id"`
		Trigger            string                             `json:"trigger"`
		CompletedAt        time.Time                          `json:"completed_at"`
		Error              string                             `json:"error,omitempty"`
		Boundary           lifecycle.TransitionBoundaryResult `json:"boundary"`
		DeletionAuthorized bool                               `json:"deletion_authorized"`
	}{
		ReceiptVersion:     qaBoundaryReceiptVersion,
		Mode:               qaBoundaryReceiptMode,
		OK:                 runErr == nil,
		JobName:            qaBoundaryJobName,
		RunID:              runID,
		Trigger:            trigger,
		CompletedAt:        completedAt,
		Boundary:           result,
		DeletionAuthorized: result.DeletionAuthorized,
	}
	if runErr != nil {
		receipt.Error = truncateErr(runErr)
	}
	if encErr := json.NewEncoder(out).Encode(receipt); encErr != nil {
		return errors.Join(runErr, fmt.Errorf("encode qa boundary receipt: %w", encErr))
	}
	return runErr
}

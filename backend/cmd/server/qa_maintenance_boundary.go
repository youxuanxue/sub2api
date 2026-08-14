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
	qaBoundaryConfirmation         = "tokenkey-prod-qa-boundary-v1"
	qaBoundaryReceiptVersion       = 1
	qaBoundaryReceiptMode          = "qa_maintenance_boundary"
	qaBoundaryJobName              = "qa-boundary"
	qaBoundaryHeartbeatTimeout     = 5 * time.Second
	qaCutoverProvisionConfirmation = "tokenkey-prod-qa-cutover-provision-v1"
	qaBoundaryConnectionOptions    = "-c lock_timeout=100ms -c statement_timeout=120s"
)

type qaBoundaryDeps struct {
	loadConfig       func() (*config.Config, error)
	openDB           func(driverName, dataSourceName string) (*sql.DB, error)
	tryAdvisoryLock  func(context.Context, *sql.Conn) (bool, error)
	unlockAdvisory   func(context.Context, *sql.Conn) error
	runBoundary      func(context.Context, *sql.DB, lifecycle.ControlStore, lifecycle.Options) (lifecycle.BoundaryResult, error)
	runProvisionOnly func(context.Context, lifecycle.DB, lifecycle.Options) (lifecycle.ProvisionResult, error)
	writeHeartbeat   func(context.Context, *sql.DB, *service.OpsUpsertJobHeartbeatInput) error
	now              func() time.Time
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
		runProvisionOnly: lifecycle.RunCutoverProvisionOnly,
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
	if d.runProvisionOnly == nil {
		d.runProvisionOnly = defaults.runProvisionOnly
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
		switch arg {
		case "--qa-boundary-once", "--qa-boundary-once=true",
			"--qa-cutover-inventory", "--qa-cutover-inventory=true",
			"--qa-cutover-plan", "--qa-cutover-plan=true",
			"--qa-cutover-apply", "--qa-cutover-apply=true",
			"--qa-cutover-provision-only", "--qa-cutover-provision-only=true",
			"--qa-cutover-finalize-plan", "--qa-cutover-finalize-plan=true",
			"--qa-cutover-finalize", "--qa-cutover-finalize=true":
			return true
		}
		if strings.HasPrefix(arg, "--qa-cutover-plan=") ||
			strings.HasPrefix(arg, "--qa-cutover-apply=") ||
			strings.HasPrefix(arg, "--qa-cutover-provision-only=") ||
			strings.HasPrefix(arg, "--qa-cutover-finalize-plan=") ||
			strings.HasPrefix(arg, "--qa-cutover-finalize=") {
			return true
		}
	}
	return false
}

func runQACutoverProvisionOnly(ctx context.Context, confirmation string, out io.Writer, deps qaBoundaryDeps) error {
	if strings.TrimSpace(confirmation) != qaCutoverProvisionConfirmation {
		return fmt.Errorf("qa cutover provision confirmation mismatch")
	}
	deps = deps.withDefaults()
	db, err := openQABoundaryDB(deps)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var result lifecycle.ProvisionResult
	if err := withQAMaintenanceLock(ctx, db, deps, func(ctx context.Context, lockedDB *sql.DB) error {
		var runErr error
		result, runErr = deps.runProvisionOnly(ctx, lockedDB, lifecycle.Options{HoursAhead: lifecycle.HourlyHorizon})
		return runErr
	}); err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return fmt.Errorf("encode qa cutover provision result: %w", err)
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

func runQACutoverInventory(ctx context.Context, out io.Writer, deps qaBoundaryDeps) error {
	db, err := openQABoundaryDB(deps)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	inv, err := lifecycle.BuildCutoverInventory(ctx, db, lifecycle.HourlyHorizon)
	if err != nil {
		return err
	}
	if err := lifecycle.AddHotFileInventory(&inv, qaBoundaryDataDir()); err != nil {
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

func qaBoundaryDataDir() string {
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		return "/app/data"
	}
	return dataDir
}

func runQACutoverPlan(ctx context.Context, t0Raw string, phase lifecycle.CutoverPhase, out io.Writer, deps qaBoundaryDeps) error {
	t0, err := lifecycle.ParseHourlyCutoverUTCStrict(t0Raw)
	if err != nil {
		return err
	}
	if t0.IsZero() {
		return fmt.Errorf("qa cutover plan requires --t0")
	}
	db, err := openQABoundaryDB(deps)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	inv, err := lifecycle.BuildCutoverInventory(ctx, db, lifecycle.HourlyHorizon)
	if err != nil {
		return err
	}
	if err := lifecycle.AddHotFileInventory(&inv, qaBoundaryDataDir()); err != nil {
		return err
	}
	var plan lifecycle.CutoverPlan
	switch phase {
	case lifecycle.CutoverPhaseActivate:
		plan, err = lifecycle.BuildCutoverPlan(inv, t0)
	case lifecycle.CutoverPhaseFinalize:
		plan, err = lifecycle.BuildCutoverFinalizePlan(inv, t0)
	default:
		return fmt.Errorf("qa cutover plan phase %q is unsupported", phase)
	}
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("encode qa cutover plan: %w", err)
	}
	if _, err := out.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write qa cutover plan: %w", err)
	}
	return nil
}

func runQACutoverApply(ctx context.Context, t0Raw, planHash, confirmation string, phase lifecycle.CutoverPhase, deps qaBoundaryDeps) error {
	t0, err := lifecycle.ParseHourlyCutoverUTCStrict(t0Raw)
	if err != nil {
		return err
	}
	if t0.IsZero() {
		return fmt.Errorf("qa cutover apply requires --t0")
	}
	if err := lifecycle.ValidateCutoverApplyConfirmation(planHash, confirmation); err != nil {
		return err
	}
	db, err := openQABoundaryDB(deps)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return withQAMaintenanceLock(ctx, db, deps, func(ctx context.Context, lockedDB *sql.DB) error {
		applied, err := lifecycle.MatchAppliedCutover(ctx, lockedDB, phase, t0, planHash)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
		inv, err := lifecycle.BuildCutoverInventory(ctx, lockedDB, lifecycle.HourlyHorizon)
		if err != nil {
			return err
		}
		if err := lifecycle.AddHotFileInventory(&inv, qaBoundaryDataDir()); err != nil {
			return err
		}
		var plan lifecycle.CutoverPlan
		switch phase {
		case lifecycle.CutoverPhaseActivate:
			plan, err = lifecycle.BuildCutoverPlan(inv, t0)
		case lifecycle.CutoverPhaseFinalize:
			plan, err = lifecycle.BuildCutoverFinalizePlan(inv, t0)
		default:
			return fmt.Errorf("qa cutover apply phase %q is unsupported", phase)
		}
		if err != nil {
			return err
		}
		if plan.PlanHash != strings.TrimSpace(planHash) {
			return fmt.Errorf("qa cutover apply plan hash drift")
		}
		return lifecycle.ApplyCutoverPlan(ctx, lockedDB, plan)
	})
}

func runQABoundaryCommand(ctx context.Context, args []string, out io.Writer, deps qaBoundaryDeps) error {
	fs := flag.NewFlagSet("qa-boundary", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var once bool
	var cutoverInventory bool
	var cutoverPlan bool
	var cutoverApply bool
	var cutoverProvisionOnly bool
	var cutoverFinalizePlan bool
	var cutoverFinalize bool
	var confirmation string
	var t0 string
	var planHash string
	fs.BoolVar(&once, "qa-boundary-once", false, "run QA lifecycle boundary maintenance and exit")
	fs.BoolVar(&cutoverInventory, "qa-cutover-inventory", false, "render read-only hourly cutover inventory JSON")
	fs.BoolVar(&cutoverPlan, "qa-cutover-plan", false, "render guarded hourly cutover plan JSON")
	fs.BoolVar(&cutoverApply, "qa-cutover-apply", false, "apply guarded hourly cutover plan under QAMA lock")
	fs.BoolVar(&cutoverProvisionOnly, "qa-cutover-provision-only", false, "extend the post-T0 hourly horizon without expiry or cleanup")
	fs.BoolVar(&cutoverFinalizePlan, "qa-cutover-finalize-plan", false, "render guarded DEFAULT-removal plan JSON")
	fs.BoolVar(&cutoverFinalize, "qa-cutover-finalize", false, "remove empty DEFAULT under the final cutover gate")
	fs.StringVar(&confirmation, "confirm", "", "exact production QA confirmation")
	fs.StringVar(&t0, "t0", "", "UTC cutover hour for plan/apply")
	fs.StringVar(&planHash, "plan-hash", "", "expected cutover plan hash for apply")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("qa boundary flags: %w", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("qa boundary does not accept positional arguments")
	}
	modeCount := 0
	for _, selected := range []bool{
		once, cutoverInventory, cutoverPlan, cutoverApply, cutoverProvisionOnly, cutoverFinalizePlan, cutoverFinalize,
	} {
		if selected {
			modeCount++
		}
	}
	if modeCount != 1 {
		return fmt.Errorf("qa boundary requires exactly one mode")
	}
	if cutoverInventory {
		return runQACutoverInventory(ctx, out, deps)
	}
	if cutoverPlan {
		return runQACutoverPlan(ctx, t0, lifecycle.CutoverPhaseActivate, out, deps)
	}
	if cutoverApply {
		return runQACutoverApply(ctx, t0, planHash, confirmation, lifecycle.CutoverPhaseActivate, deps)
	}
	if cutoverProvisionOnly {
		return runQACutoverProvisionOnly(ctx, confirmation, out, deps)
	}
	if cutoverFinalizePlan {
		return runQACutoverPlan(ctx, t0, lifecycle.CutoverPhaseFinalize, out, deps)
	}
	if cutoverFinalize {
		return runQACutoverApply(ctx, t0, planHash, confirmation, lifecycle.CutoverPhaseFinalize, deps)
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
	var result lifecycle.BoundaryResult
	var runErr error
	lockErr := withQAMaintenanceLock(ctx, db, deps, func(ctx context.Context, lockedDB *sql.DB) error {
		dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
		if dataDir == "" {
			dataDir = "/app/data"
		}
		result, runErr = deps.runBoundary(ctx, lockedDB, lifecycle.NewSQLControlAdapter(archive.NewSQLControlStore()), lifecycle.Options{
			HoursAhead: lifecycle.HourlyHorizon,
			BlobRoot:   dataDir,
			DLQRoot:    dataDir,
		})
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
	if result.Expiry != nil && result.Expiry.PartitionName != "" {
		lastResult += fmt.Sprintf(" dropped=%s terminal_gap=%t", result.Expiry.PartitionName, result.Expiry.TerminalGap)
	}
	if len(result.Expiries) > 1 {
		lastResult += fmt.Sprintf(" drops=%d", len(result.Expiries))
	}
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
		ReceiptVersion     int                      `json:"receipt_version"`
		Mode               string                   `json:"mode"`
		OK                 bool                     `json:"ok"`
		JobName            string                   `json:"job_name"`
		RunID              string                   `json:"run_id"`
		Trigger            string                   `json:"trigger"`
		CompletedAt        time.Time                `json:"completed_at"`
		Error              string                   `json:"error,omitempty"`
		Boundary           lifecycle.BoundaryResult `json:"boundary"`
		DeletionAuthorized bool                     `json:"deletion_authorized"`
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

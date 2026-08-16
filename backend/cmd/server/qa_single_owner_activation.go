package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/captureledger"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/lifecycle"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

const (
	qaSingleOwnerActivationConfirmationPrefix = "tokenkey-prod-qa-single-owner-activate-v1:"
	qaSingleOwnerActivationAckSchema          = "qa-single-owner-host-ack-v1"
	qaSingleOwnerActivationReadySchema        = "qa-single-owner-db-lock-ready-v1"
	qaSingleOwnerActivationPlanSchema         = "qa-single-owner-activation-plan-v1"
	qaSingleOwnerActivationDefaultAckTimeout  = 5 * time.Minute
	qaSingleOwnerActivationAckFreshness       = 30 * time.Second
)

var (
	qaSingleOwnerActivationPlanHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	qaSingleOwnerActivationRunIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type qaSingleOwnerActivationDeps struct {
	loadConfig      func() (*config.Config, error)
	openDB          func(driverName, dataSourceName string) (*sql.DB, error)
	tryAdvisoryLock func(context.Context, *sql.Conn) (bool, error)
	unlockAdvisory  func(context.Context, *sql.Conn) error
	now             func() time.Time
	nonce           func() (string, error)
	buildPlan       func(context.Context, *sql.Conn, time.Time) (qaSingleOwnerActivationPlan, error)
}

type qaSingleOwnerActivationPlanPartition struct {
	Schema            string    `json:"schema"`
	Name              string    `json:"name"`
	Lower             time.Time `json:"lower_utc"`
	Upper             time.Time `json:"upper_utc"`
	ArchiveShardID    int64     `json:"archive_shard_id"`
	ArchiveCommitKey  string    `json:"archive_commit_key"`
	ArchiveChecksums  string    `json:"archive_checksums_sha256"`
	RestoreVerifiedAt time.Time `json:"restore_verified_at"`
	CaptureSealDigest string    `json:"capture_seal_digest"`
}

type qaSingleOwnerActivationPlan struct {
	SchemaVersion       string                                 `json:"schema_version"`
	DatabaseHour        time.Time                              `json:"database_hour_utc"`
	CompletedPartitions []qaSingleOwnerActivationPlanPartition `json:"completed_partitions"`
	PlanHash            string                                 `json:"plan_hash"`
}

type qaSingleOwnerActivationReady struct {
	SchemaVersion string    `json:"schema_version"`
	RunID         string    `json:"run_id"`
	Nonce         string    `json:"nonce"`
	PlanHash      string    `json:"plan_hash"`
	DatabaseLock  bool      `json:"database_lock_acquired"`
	ReadyAt       time.Time `json:"ready_at"`
}

type qaSingleOwnerActivationHostAck struct {
	SchemaVersion         string    `json:"schema_version"`
	RunID                 string    `json:"run_id"`
	Nonce                 string    `json:"nonce"`
	PlanHash              string    `json:"plan_hash"`
	BoundaryTimerEnabled  bool      `json:"boundary_timer_enabled"`
	BoundaryTimerActive   bool      `json:"boundary_timer_active"`
	BoundaryServiceActive bool      `json:"boundary_service_active"`
	CheckedAt             time.Time `json:"checked_at"`
}

type qaSingleOwnerActivationReceipt struct {
	OK          bool      `json:"ok"`
	Phase       string    `json:"phase"`
	RunID       string    `json:"run_id"`
	PlanHash    string    `json:"plan_hash"`
	T0          time.Time `json:"t0_utc"`
	ActivatedAt time.Time `json:"activated_at"`
}

func defaultQASingleOwnerActivationDeps() qaSingleOwnerActivationDeps {
	return qaSingleOwnerActivationDeps{
		loadConfig: config.LoadForBootstrap,
		openDB:     sql.Open,
		tryAdvisoryLock: func(ctx context.Context, conn *sql.Conn) (bool, error) {
			var locked bool
			err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", qaMaintenanceAdvisoryLockID).Scan(&locked)
			return locked, err
		},
		unlockAdvisory: func(ctx context.Context, conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", qaMaintenanceAdvisoryLockID)
			return err
		},
		now: time.Now,
		nonce: func() (string, error) {
			var raw [32]byte
			if _, err := rand.Read(raw[:]); err != nil {
				return "", err
			}
			return hex.EncodeToString(raw[:]), nil
		},
		buildPlan: buildQASingleOwnerActivationPlan,
	}
}

func (d qaSingleOwnerActivationDeps) withDefaults() qaSingleOwnerActivationDeps {
	defaults := defaultQASingleOwnerActivationDeps()
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
	if d.now == nil {
		d.now = defaults.now
	}
	if d.nonce == nil {
		d.nonce = defaults.nonce
	}
	if d.buildPlan == nil {
		d.buildPlan = defaults.buildPlan
	}
	return d
}

func qaSingleOwnerActivationRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--qa-single-owner-activate" || arg == "--qa-single-owner-activate=true" ||
			arg == "--qa-single-owner-plan" || arg == "--qa-single-owner-plan=true" {
			return true
		}
	}
	return false
}

func runQASingleOwnerActivationCommand(
	ctx context.Context,
	args []string,
	out io.Writer,
	deps qaSingleOwnerActivationDeps,
) (resultErr error) {
	fs := flag.NewFlagSet("qa-single-owner-activate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var activate bool
	var renderPlan bool
	var planHash string
	var confirmation string
	fs.BoolVar(&activate, "qa-single-owner-activate", false, "activate maintenance as the only QA lifecycle owner")
	fs.BoolVar(&renderPlan, "qa-single-owner-plan", false, "render the read-only activation readiness plan")
	fs.StringVar(&planHash, "plan-hash", "", "approved host activation plan hash")
	fs.StringVar(&confirmation, "confirm", "", "hash-bound production activation confirmation")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("qa single-owner activation flags: %w", err)
	}
	if fs.NArg() != 0 || activate == renderPlan {
		return errors.New("qa single-owner command requires exactly one mode")
	}
	deps = deps.withDefaults()
	if renderPlan {
		return runQASingleOwnerActivationPlan(ctx, out, deps)
	}
	planHash = strings.TrimSpace(planHash)
	if !qaSingleOwnerActivationPlanHashPattern.MatchString(planHash) {
		return errors.New("qa single-owner activation plan hash is invalid")
	}
	if confirmation != qaSingleOwnerActivationConfirmationPrefix+planHash {
		return errors.New("qa single-owner activation confirmation mismatch")
	}
	runID := strings.TrimSpace(os.Getenv("QA_SINGLE_OWNER_ACTIVATION_RUN_ID"))
	if !qaSingleOwnerActivationRunIDPattern.MatchString(runID) {
		return errors.New("qa single-owner activation run id is invalid")
	}
	activationDir := strings.TrimSpace(os.Getenv("QA_SINGLE_OWNER_ACTIVATION_DIR"))
	if activationDir == "" {
		activationDir = filepath.Join(qaDataRoot(), "qa_single_owner_activation")
	}
	if filepath.Clean(activationDir) != activationDir {
		return errors.New("qa single-owner activation directory is noncanonical")
	}
	ackTimeout, err := qaSingleOwnerActivationAckTimeout()
	if err != nil {
		return err
	}

	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load qa single-owner activation config: %w", err)
	}
	db, err := deps.openDB("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return fmt.Errorf("open qa single-owner activation database: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire qa single-owner activation connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.PingContext(ctx); err != nil {
		return fmt.Errorf("ping qa single-owner activation database: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET lock_timeout = '100ms'"); err != nil {
		return fmt.Errorf("set qa single-owner activation lock timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET statement_timeout = '120s'"); err != nil {
		return fmt.Errorf("set qa single-owner activation statement timeout: %w", err)
	}
	locked, err := deps.tryAdvisoryLock(ctx, conn)
	if err != nil {
		return fmt.Errorf("acquire qa maintenance advisory lock for activation: %w", err)
	}
	if !locked {
		return errors.New("qa maintenance advisory lock already held")
	}
	defer func() {
		if unlockErr := deps.unlockAdvisory(context.Background(), conn); unlockErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("release qa maintenance advisory lock after activation: %w", unlockErr)
		}
	}()
	lockedPlan, err := deps.buildPlan(ctx, conn, deps.now().UTC())
	if err != nil {
		return fmt.Errorf("build qa single-owner activation plan under lock: %w", err)
	}
	if lockedPlan.PlanHash != planHash {
		return errors.New("qa single-owner activation plan hash drift")
	}

	nonce, err := deps.nonce()
	if err != nil {
		return fmt.Errorf("generate qa single-owner activation nonce: %w", err)
	}
	ready := qaSingleOwnerActivationReady{
		SchemaVersion: qaSingleOwnerActivationReadySchema,
		RunID:         runID,
		Nonce:         nonce,
		PlanHash:      planHash,
		DatabaseLock:  true,
		ReadyAt:       deps.now().UTC(),
	}
	if err := writeQAActivationJSON(activationDir, runID+".ready.json", ready); err != nil {
		return fmt.Errorf("write qa single-owner activation ready receipt: %w", err)
	}
	ack, err := waitForQAActivationHostAck(ctx, filepath.Join(activationDir, runID+".ack.json"), ackTimeout)
	if err != nil {
		return err
	}
	if err := validateQAActivationHostAck(ack, runID, nonce, planHash, deps.now().UTC()); err != nil {
		return err
	}
	recheckedPlan, err := deps.buildPlan(ctx, conn, deps.now().UTC())
	if err != nil {
		return fmt.Errorf("rebuild qa single-owner activation plan after host acknowledgement: %w", err)
	}
	if recheckedPlan.PlanHash != planHash {
		return errors.New("qa single-owner activation plan hash drift after host acknowledgement")
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin qa single-owner activation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var t0 time.Time
	if err := tx.QueryRowContext(ctx, "SELECT date_trunc('hour', clock_timestamp())").Scan(&t0); err != nil {
		return fmt.Errorf("read qa single-owner activation database hour: %w", err)
	}
	t0 = t0.UTC()
	result, err := tx.ExecContext(ctx, `
INSERT INTO qa_lifecycle_receipts (phase, plan_hash, t0_utc)
VALUES ('single_owner_activate', $1, $2)
ON CONFLICT (phase) DO NOTHING`, planHash, t0)
	if err != nil {
		return fmt.Errorf("insert qa single-owner activation receipt: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect qa single-owner activation insert: %w", err)
	}
	if changed != 1 {
		var existingPlanHash string
		var existingT0 time.Time
		if err := tx.QueryRowContext(ctx, `
SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts WHERE phase = $1`, "single_owner_activate").Scan(&existingPlanHash, &existingT0); err != nil {
			return fmt.Errorf("read existing qa single-owner activation receipt: %w", err)
		}
		if existingPlanHash != planHash {
			return errors.New("qa single-owner activation receipt already exists for a different plan")
		}
		t0 = existingT0.UTC()
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit qa single-owner activation receipt: %w", err)
	}
	receipt := qaSingleOwnerActivationReceipt{
		OK:          true,
		Phase:       "single_owner_activate",
		RunID:       runID,
		PlanHash:    planHash,
		T0:          t0,
		ActivatedAt: deps.now().UTC(),
	}
	if err := json.NewEncoder(out).Encode(receipt); err != nil {
		return fmt.Errorf("encode qa single-owner activation receipt: %w", err)
	}
	return nil
}

func runQASingleOwnerActivationPlan(ctx context.Context, out io.Writer, deps qaSingleOwnerActivationDeps) error {
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load qa single-owner activation plan config: %w", err)
	}
	db, err := deps.openDB("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return fmt.Errorf("open qa single-owner activation plan database: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire qa single-owner activation plan connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.PingContext(ctx); err != nil {
		return fmt.Errorf("ping qa single-owner activation plan database: %w", err)
	}
	plan, err := deps.buildPlan(ctx, conn, deps.now().UTC())
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(plan); err != nil {
		return fmt.Errorf("encode qa single-owner activation plan: %w", err)
	}
	return nil
}

func buildQASingleOwnerActivationPlan(
	ctx context.Context,
	conn *sql.Conn,
	observedAt time.Time,
) (qaSingleOwnerActivationPlan, error) {
	var plan qaSingleOwnerActivationPlan
	plan.SchemaVersion = qaSingleOwnerActivationPlanSchema
	if conn == nil {
		return plan, errors.New("qa single-owner activation plan database connection is required")
	}
	if err := conn.QueryRowContext(ctx, "SELECT date_trunc('hour', clock_timestamp())").Scan(&plan.DatabaseHour); err != nil {
		return plan, fmt.Errorf("read qa single-owner activation database hour: %w", err)
	}
	plan.DatabaseHour = plan.DatabaseHour.UTC()
	children, err := pgpartition.ListChildPartitionBounds(ctx, conn, lifecycle.TableQARecords)
	if err != nil {
		return plan, err
	}
	catalogHours := make(map[time.Time]struct{}, len(children))
	for _, child := range children {
		if child.IsDefault {
			return plan, fmt.Errorf("qa single-owner activation refuses DEFAULT partition %s.%s", child.Schema, child.Name)
		}
		catalogHours[child.Lower.UTC()] = struct{}{}
	}
	for hour := pgpartition.RetentionBoundary(plan.DatabaseHour); hour.Before(plan.DatabaseHour); hour = hour.Add(time.Hour) {
		if _, ok := catalogHours[hour]; !ok {
			return plan, fmt.Errorf("qa single-owner activation missing required recent partition at %s", hour.Format(time.RFC3339))
		}
	}
	for _, required := range pgpartition.HourlyTargetRanges(plan.DatabaseHour, pgpartition.QARecordsHourlyHorizon) {
		if _, ok := catalogHours[required.Start]; !ok {
			return plan, fmt.Errorf("qa single-owner activation missing required current/future partition at %s", required.Start.Format(time.RFC3339))
		}
	}
	control := archive.NewSQLControlStore()
	for _, child := range children {
		if child.Upper.After(plan.DatabaseHour) {
			continue
		}
		window := archive.Window{Start: child.Lower.UTC(), End: child.Upper.UTC()}
		status, err := control.InspectCatchupHour(ctx, conn, window)
		if err != nil {
			return plan, fmt.Errorf("inspect activation archive readiness for %s: %w", child.Name, err)
		}
		if !status.Exists || status.ShardID == 0 || status.State != archive.StateCommitted || !status.RestoreVerified || status.UncoveredSourceExists {
			return plan, fmt.Errorf("qa single-owner activation partition %s is not committed, restore-verified, and fully covered", child.Name)
		}
		var commitKey string
		var checksums string
		var restoreVerifiedAt time.Time
		if err := conn.QueryRowContext(ctx, `
SELECT COALESCE(commit_key, ''), checksums::text, restore_verified_at
FROM qa_archive_shards
WHERE id = $1 AND state = 'committed'`, status.ShardID).Scan(&commitKey, &checksums, &restoreVerifiedAt); err != nil {
			return plan, fmt.Errorf("read activation archive commit for %s: %w", child.Name, err)
		}
		if strings.TrimSpace(commitKey) == "" || restoreVerifiedAt.IsZero() {
			return plan, fmt.Errorf("qa single-owner activation partition %s has incomplete archive commit metadata", child.Name)
		}
		seal, err := captureledger.ValidateHourSeal(qaCaptureLedgerRoot(), child.Lower, observedAt.UTC(), captureledger.DefaultFreshness)
		if err != nil {
			return plan, fmt.Errorf("validate activation capture seal for %s: %w", child.Name, err)
		}
		checksumHash := sha256.Sum256([]byte(checksums))
		plan.CompletedPartitions = append(plan.CompletedPartitions, qaSingleOwnerActivationPlanPartition{
			Schema: child.Schema, Name: child.Name, Lower: child.Lower.UTC(), Upper: child.Upper.UTC(),
			ArchiveShardID: status.ShardID, ArchiveCommitKey: commitKey,
			ArchiveChecksums: hex.EncodeToString(checksumHash[:]), RestoreVerifiedAt: restoreVerifiedAt.UTC(),
			CaptureSealDigest: seal.StateDigest,
		})
	}
	sort.Slice(plan.CompletedPartitions, func(i, j int) bool {
		left, right := plan.CompletedPartitions[i], plan.CompletedPartitions[j]
		if !left.Lower.Equal(right.Lower) {
			return left.Lower.Before(right.Lower)
		}
		if left.Schema != right.Schema {
			return left.Schema < right.Schema
		}
		return left.Name < right.Name
	})
	payload := struct {
		SchemaVersion       string                                 `json:"schema_version"`
		DatabaseHour        time.Time                              `json:"database_hour_utc"`
		CompletedPartitions []qaSingleOwnerActivationPlanPartition `json:"completed_partitions"`
	}{plan.SchemaVersion, plan.DatabaseHour, plan.CompletedPartitions}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return plan, fmt.Errorf("encode qa single-owner activation plan hash payload: %w", err)
	}
	hash := sha256.Sum256(encoded)
	plan.PlanHash = hex.EncodeToString(hash[:])
	return plan, nil
}

func qaSingleOwnerActivationAckTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("QA_SINGLE_OWNER_ACTIVATION_ACK_TIMEOUT_SECONDS"))
	if raw == "" {
		return qaSingleOwnerActivationDefaultAckTimeout, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 || seconds > int((30*time.Minute)/time.Second) {
		return 0, errors.New("QA_SINGLE_OWNER_ACTIVATION_ACK_TIMEOUT_SECONDS is invalid")
	}
	return time.Duration(seconds) * time.Second, nil
}

func waitForQAActivationHostAck(ctx context.Context, path string, timeout time.Duration) (qaSingleOwnerActivationHostAck, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		body, err := os.ReadFile(path)
		if err == nil {
			var ack qaSingleOwnerActivationHostAck
			if err := json.Unmarshal(body, &ack); err != nil {
				return ack, fmt.Errorf("decode qa single-owner host acknowledgement: %w", err)
			}
			return ack, nil
		}
		if !os.IsNotExist(err) {
			return qaSingleOwnerActivationHostAck{}, fmt.Errorf("read qa single-owner host acknowledgement: %w", err)
		}
		select {
		case <-ctx.Done():
			return qaSingleOwnerActivationHostAck{}, ctx.Err()
		case <-deadline.C:
			return qaSingleOwnerActivationHostAck{}, errors.New("qa single-owner host acknowledgement timed out")
		case <-ticker.C:
		}
	}
}

func validateQAActivationHostAck(ack qaSingleOwnerActivationHostAck, runID, nonce, planHash string, now time.Time) error {
	if ack.SchemaVersion != qaSingleOwnerActivationAckSchema || ack.RunID != runID || ack.Nonce != nonce || ack.PlanHash != planHash {
		return errors.New("qa single-owner host acknowledgement identity mismatch")
	}
	if ack.BoundaryTimerEnabled || ack.BoundaryTimerActive || ack.BoundaryServiceActive {
		return errors.New("qa single-owner host acknowledgement reports an active boundary owner")
	}
	if ack.CheckedAt.IsZero() || ack.CheckedAt.After(now.Add(5*time.Second)) || now.Sub(ack.CheckedAt) > qaSingleOwnerActivationAckFreshness {
		return errors.New("qa single-owner host acknowledgement is stale")
	}
	return nil
}

func writeQAActivationJSON(dir, name string, value any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".qa-activation-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(dir, name))
}

package main

import (
	"context"
	"crypto/rand"
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
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	qaSingleOwnerActivationConfirmationPrefix = "tokenkey-prod-qa-single-owner-activate-v1:"
	qaSingleOwnerActivationAckSchema          = "qa-single-owner-host-ack-v1"
	qaSingleOwnerActivationReadySchema        = "qa-single-owner-db-lock-ready-v1"
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
	return d
}

func qaSingleOwnerActivationRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--qa-single-owner-activate" || arg == "--qa-single-owner-activate=true" {
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
	var planHash string
	var confirmation string
	fs.BoolVar(&activate, "qa-single-owner-activate", false, "activate maintenance as the only QA lifecycle owner")
	fs.StringVar(&planHash, "plan-hash", "", "approved host activation plan hash")
	fs.StringVar(&confirmation, "confirm", "", "hash-bound production activation confirmation")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("qa single-owner activation flags: %w", err)
	}
	if !activate || fs.NArg() != 0 {
		return errors.New("qa single-owner activation mode was not requested")
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

	deps = deps.withDefaults()
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

//go:build unit

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

func TestQASingleOwnerActivationRejectsUnboundConfirmationBeforeDependencies(t *testing.T) {
	loaded := false
	err := runQASingleOwnerActivationCommand(
		context.Background(),
		[]string{"--qa-single-owner-activate", "--plan-hash", strings.Repeat("a", 64), "--confirm", "wrong"},
		&bytes.Buffer{},
		qaSingleOwnerActivationDeps{
			loadConfig: func() (*config.Config, error) {
				loaded = true
				return nil, nil
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("error=%v", err)
	}
	if loaded {
		t.Fatal("invalid confirmation reached configuration dependency")
	}
}

func TestQASingleOwnerActivationRechecksHostStateAfterDatabaseLock(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	planHash := strings.Repeat("b", 64)
	t0 := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	activationDir := t.TempDir()
	runID := "activate-run-1"
	t.Setenv("QA_SINGLE_OWNER_ACTIVATION_DIR", activationDir)
	t.Setenv("QA_SINGLE_OWNER_ACTIVATION_RUN_ID", runID)
	t.Setenv("QA_SINGLE_OWNER_ACTIVATION_ACK_TIMEOUT_SECONDS", "5")

	mock.ExpectPing()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT pg_try_advisory_lock").
		WithArgs(qaMaintenanceAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT date_trunc\\('hour', clock_timestamp\\(\\)\\)").
		WillReturnRows(sqlmock.NewRows([]string{"t0"}).AddRow(t0))
	mock.ExpectExec("INSERT INTO qa_lifecycle_receipts").
		WithArgs(planHash, t0).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectExec("SELECT pg_advisory_unlock").
		WithArgs(qaMaintenanceAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	ackErr := make(chan error, 1)
	go func() {
		readyPath := filepath.Join(activationDir, runID+".ready.json")
		deadline := time.Now().Add(3 * time.Second)
		for {
			body, readErr := os.ReadFile(readyPath)
			if readErr == nil {
				var ready struct {
					Nonce    string `json:"nonce"`
					PlanHash string `json:"plan_hash"`
				}
				if decodeErr := json.Unmarshal(body, &ready); decodeErr != nil {
					ackErr <- decodeErr
					return
				}
				if ready.PlanHash != planHash || ready.Nonce == "" {
					ackErr <- &activationTestError{"unexpected ready receipt"}
					return
				}
				ack := map[string]any{
					"schema_version":          "qa-single-owner-host-ack-v1",
					"run_id":                  runID,
					"nonce":                   ready.Nonce,
					"plan_hash":               planHash,
					"boundary_timer_enabled":  false,
					"boundary_timer_active":   false,
					"boundary_service_active": false,
					"checked_at":              time.Now().UTC(),
				}
				encoded, encodeErr := json.Marshal(ack)
				if encodeErr != nil {
					ackErr <- encodeErr
					return
				}
				ackErr <- os.WriteFile(filepath.Join(activationDir, runID+".ack.json"), encoded, 0o600)
				return
			}
			if !os.IsNotExist(readErr) {
				ackErr <- readErr
				return
			}
			if time.Now().After(deadline) {
				ackErr <- &activationTestError{"ready receipt timeout"}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	out := &bytes.Buffer{}
	err = runQASingleOwnerActivationCommand(
		context.Background(),
		[]string{
			"--qa-single-owner-activate",
			"--plan-hash", planHash,
			"--confirm", qaSingleOwnerActivationConfirmationPrefix + planHash,
		},
		out,
		qaSingleOwnerActivationDeps{
			loadConfig: func() (*config.Config, error) {
				return &config.Config{
					Timezone: "UTC",
					Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
				}, nil
			},
			openDB: func(string, string) (*sql.DB, error) { return db, nil },
			buildPlan: func(context.Context, *sql.Conn, time.Time) (qaSingleOwnerActivationPlan, error) {
				return qaSingleOwnerActivationPlan{PlanHash: planHash}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-ackErr; err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		OK       bool      `json:"ok"`
		PlanHash string    `json:"plan_hash"`
		T0       time.Time `json:"t0_utc"`
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.OK || receipt.PlanHash != planHash || !receipt.T0.Equal(t0) {
		t.Fatalf("receipt=%+v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQASingleOwnerActivationRetryAcceptsExistingMatchingPlan(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	planHash := strings.Repeat("c", 64)
	t0 := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	activationDir := t.TempDir()
	runID := "activate-retry-1"
	t.Setenv("QA_SINGLE_OWNER_ACTIVATION_DIR", activationDir)
	t.Setenv("QA_SINGLE_OWNER_ACTIVATION_RUN_ID", runID)
	t.Setenv("QA_SINGLE_OWNER_ACTIVATION_ACK_TIMEOUT_SECONDS", "5")

	mock.ExpectPing()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(qaMaintenanceAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT date_trunc\\('hour', clock_timestamp\\(\\)\\)").
		WillReturnRows(sqlmock.NewRows([]string{"t0"}).AddRow(t0))
	mock.ExpectExec("INSERT INTO qa_lifecycle_receipts").WithArgs(planHash, t0).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts").WithArgs("single_owner_activate").
		WillReturnRows(sqlmock.NewRows([]string{"plan_hash", "t0_utc"}).AddRow(planHash, t0))
	mock.ExpectCommit()
	mock.ExpectExec("SELECT pg_advisory_unlock").WithArgs(qaMaintenanceAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	writeActivationAckWhenReady(activationDir, runID, planHash)
	out := &bytes.Buffer{}
	err = runQASingleOwnerActivationCommand(context.Background(), []string{
		"--qa-single-owner-activate", "--plan-hash", planHash,
		"--confirm", qaSingleOwnerActivationConfirmationPrefix + planHash,
	}, out, qaSingleOwnerActivationDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{Timezone: "UTC", Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"}}, nil
		},
		openDB: func(string, string) (*sql.DB, error) { return db, nil },
		buildPlan: func(context.Context, *sql.Conn, time.Time) (qaSingleOwnerActivationPlan, error) {
			return qaSingleOwnerActivationPlan{PlanHash: planHash}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var receipt qaSingleOwnerActivationReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.OK || receipt.PlanHash != planHash || !receipt.T0.Equal(t0) {
		t.Fatalf("receipt=%+v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQASingleOwnerActivationRejectsPlanDriftAfterHostAck(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	planHash := strings.Repeat("d", 64)
	activationDir := t.TempDir()
	runID := "activate-plan-drift"
	t.Setenv("QA_SINGLE_OWNER_ACTIVATION_DIR", activationDir)
	t.Setenv("QA_SINGLE_OWNER_ACTIVATION_RUN_ID", runID)
	t.Setenv("QA_SINGLE_OWNER_ACTIVATION_ACK_TIMEOUT_SECONDS", "5")

	mock.ExpectPing()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(qaMaintenanceAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("SELECT pg_advisory_unlock").WithArgs(qaMaintenanceAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	buildCalls := 0
	writeActivationAckWhenReady(activationDir, runID, planHash)
	err = runQASingleOwnerActivationCommand(context.Background(), []string{
		"--qa-single-owner-activate", "--plan-hash", planHash,
		"--confirm", qaSingleOwnerActivationConfirmationPrefix + planHash,
	}, &bytes.Buffer{}, qaSingleOwnerActivationDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{Timezone: "UTC", Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"}}, nil
		},
		openDB: func(string, string) (*sql.DB, error) { return db, nil },
		buildPlan: func(context.Context, *sql.Conn, time.Time) (qaSingleOwnerActivationPlan, error) {
			buildCalls++
			hash := planHash
			if buildCalls == 2 {
				hash = strings.Repeat("e", 64)
			}
			return qaSingleOwnerActivationPlan{PlanHash: hash}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "plan hash drift") {
		t.Fatalf("err=%v", err)
	}
	if buildCalls != 2 {
		t.Fatalf("buildCalls=%d", buildCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQASingleOwnerActivationPlanRejectsCompletedPartitionWithoutArchiveCommit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	databaseHour := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	hour := databaseHour.Add(-24 * time.Hour)
	mock.ExpectQuery("SELECT date_trunc").WillReturnRows(sqlmock.NewRows([]string{"hour"}).AddRow(databaseHour))
	mock.ExpectQuery("FROM pg_inherits").WithArgs("qa_records").WillReturnRows(activationPartitionRows(databaseHour))
	mock.ExpectQuery("LEFT JOIN qa_archive_shards").WithArgs(hour, hour.Add(time.Hour)).WillReturnRows(
		sqlmock.NewRows([]string{"exists", "id", "window_end", "state", "restore", "code", "source", "uncovered"}).
			AddRow(false, int64(0), nil, "", false, "", true, true),
	)

	_, err = buildQASingleOwnerActivationPlan(context.Background(), conn, databaseHour.Add(10*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "not committed") {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQASingleOwnerActivationPlanRejectsCompletedPartitionWithoutCaptureSeal(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	t.Setenv("DATA_DIR", t.TempDir())
	databaseHour := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	hour := databaseHour.Add(-24 * time.Hour)
	restoredAt := databaseHour.Add(-30 * time.Minute)
	mock.ExpectQuery("SELECT date_trunc").WillReturnRows(sqlmock.NewRows([]string{"hour"}).AddRow(databaseHour))
	mock.ExpectQuery("FROM pg_inherits").WithArgs("qa_records").WillReturnRows(activationPartitionRows(databaseHour))
	mock.ExpectQuery("LEFT JOIN qa_archive_shards").WithArgs(hour, hour.Add(time.Hour)).WillReturnRows(
		sqlmock.NewRows([]string{"exists", "id", "window_end", "state", "restore", "code", "source", "uncovered"}).
			AddRow(true, int64(7), hour.Add(time.Hour), "committed", true, "", true, false),
	)
	mock.ExpectQuery("FROM qa_archive_shards").WithArgs(int64(7)).WillReturnRows(
		sqlmock.NewRows([]string{"commit_key", "checksums", "restore_verified_at"}).
			AddRow("qa-archive/v1/hour=2026-08-15T10/commit.json", `{}`, restoredAt),
	)

	_, err = buildQASingleOwnerActivationPlan(context.Background(), conn, databaseHour.Add(10*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "capture seal") {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQASingleOwnerActivationPlanRejectsMissingRecentCompletedHour(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	databaseHour := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	missingHour := databaseHour.Add(-7 * time.Hour)
	rows := sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	})
	for offset := 24; offset >= 1; offset-- {
		hour := databaseHour.Add(-time.Duration(offset) * time.Hour)
		if hour.Equal(missingHour) {
			continue
		}
		rows.AddRow(
			"public", "qa_records_"+hour.UTC().Format("20060102_15"),
			"FOR VALUES FROM ('"+hour.Format(time.RFC3339)+"') TO ('"+hour.Add(time.Hour).Format(time.RFC3339)+"')",
			false, false, false, hour, hour.Add(time.Hour),
		)
	}
	mock.ExpectQuery("SELECT date_trunc").WillReturnRows(sqlmock.NewRows([]string{"hour"}).AddRow(databaseHour))
	mock.ExpectQuery("FROM pg_inherits").WithArgs("qa_records").WillReturnRows(rows)

	_, err = buildQASingleOwnerActivationPlan(context.Background(), conn, databaseHour.Add(10*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "missing required recent partition") || !strings.Contains(err.Error(), missingHour.Format(time.RFC3339)) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQASingleOwnerActivationPlanRejectsMissingCurrentOrFutureHour(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	databaseHour := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	missingHour := databaseHour.Add(7 * time.Hour)
	rows := sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	})
	for offset := -24; offset < pgpartition.QARecordsHourlyHorizon; offset++ {
		hour := databaseHour.Add(time.Duration(offset) * time.Hour)
		if hour.Equal(missingHour) {
			continue
		}
		rows.AddRow(
			"public", "qa_records_"+hour.UTC().Format("20060102_15"),
			"FOR VALUES FROM ('"+hour.Format(time.RFC3339)+"') TO ('"+hour.Add(time.Hour).Format(time.RFC3339)+"')",
			false, false, false, hour, hour.Add(time.Hour),
		)
	}
	mock.ExpectQuery("SELECT date_trunc").WillReturnRows(sqlmock.NewRows([]string{"hour"}).AddRow(databaseHour))
	mock.ExpectQuery("FROM pg_inherits").WithArgs("qa_records").WillReturnRows(rows)

	_, err = buildQASingleOwnerActivationPlan(context.Background(), conn, databaseHour.Add(10*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "missing required current/future partition") || !strings.Contains(err.Error(), missingHour.Format(time.RFC3339)) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQASingleOwnerActivationPlanRejectsMalformedCurrentOrFutureHour(t *testing.T) {
	databaseHour := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name         string
		offset       int
		lower        time.Time
		upper        time.Time
		wantContains string
	}{
		{name: "short current hour", offset: 0, lower: databaseHour, upper: databaseHour.Add(30 * time.Minute), wantContains: "exactly one hour"},
		{name: "wide future hour", offset: 7, lower: databaseHour.Add(7 * time.Hour), upper: databaseHour.Add(9 * time.Hour), wantContains: "exactly one hour"},
		{name: "misaligned future hour", offset: 7, lower: databaseHour.Add(7*time.Hour + 30*time.Minute), upper: databaseHour.Add(8*time.Hour + 30*time.Minute), wantContains: "canonical hourly child name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()

			rows := sqlmock.NewRows([]string{
				"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
			})
			for offset := -24; offset < pgpartition.QARecordsHourlyHorizon; offset++ {
				hour := databaseHour.Add(time.Duration(offset) * time.Hour)
				upper := hour.Add(time.Hour)
				if offset == test.offset {
					hour = test.lower
					upper = test.upper
				}
				rows.AddRow(
					"public", "qa_records_"+hour.UTC().Format("20060102_15"),
					"FOR VALUES FROM ('"+hour.Format(time.RFC3339)+"') TO ('"+upper.Format(time.RFC3339)+"')",
					false, false, false, hour, upper,
				)
			}
			mock.ExpectQuery("SELECT date_trunc").WillReturnRows(sqlmock.NewRows([]string{"hour"}).AddRow(databaseHour))
			mock.ExpectQuery("FROM pg_inherits").WithArgs("qa_records").WillReturnRows(rows)

			_, err = buildQASingleOwnerActivationPlan(context.Background(), conn, databaseHour.Add(10*time.Minute))
			if err == nil || !strings.Contains(err.Error(), test.wantContains) {
				t.Fatalf("err=%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func activationPartitionRows(databaseHour time.Time) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	})
	for offset := -24; offset < pgpartition.QARecordsHourlyHorizon; offset++ {
		hour := databaseHour.Add(time.Duration(offset) * time.Hour)
		rows.AddRow(
			"public", "qa_records_"+hour.UTC().Format("20060102_15"),
			"FOR VALUES FROM ('"+hour.Format(time.RFC3339)+"') TO ('"+hour.Add(time.Hour).Format(time.RFC3339)+"')",
			false, false, false, hour, hour.Add(time.Hour),
		)
	}
	return rows
}

func writeActivationAckWhenReady(dir, runID, planHash string) {
	go func() {
		readyPath := filepath.Join(dir, runID+".ready.json")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			body, err := os.ReadFile(readyPath)
			if err == nil {
				var ready qaSingleOwnerActivationReady
				if json.Unmarshal(body, &ready) == nil {
					ack := qaSingleOwnerActivationHostAck{
						SchemaVersion: qaSingleOwnerActivationAckSchema,
						RunID:         runID, Nonce: ready.Nonce, PlanHash: planHash,
						CheckedAt: time.Now().UTC(),
					}
					encoded, _ := json.Marshal(ack)
					_ = os.WriteFile(filepath.Join(dir, runID+".ack.json"), encoded, 0o600)
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
}

type activationTestError struct{ message string }

func (e *activationTestError) Error() string { return e.message }

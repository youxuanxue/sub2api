//go:build unit

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/lifecycle"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestQABoundaryLockContentionAdvancesFailureHeartbeatAndReceipt(t *testing.T) {
	t.Setenv("QA_MAINTENANCE_RUN_ID", "boundary-lock-run")
	t.Setenv("QA_MAINTENANCE_TRIGGER", "timer")
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	var heartbeat *service.OpsUpsertJobHeartbeatInput
	deps := qaBoundaryDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone: "UTC",
				Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
			}, nil
		},
		openDB: func(string, string) (*sql.DB, error) { return db, nil },
		tryAdvisoryLock: func(context.Context, *sql.Conn) (bool, error) {
			return false, nil
		},
		writeHeartbeat: func(_ context.Context, gotDB *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
			if gotDB.Stats().InUse != 0 {
				t.Fatalf("heartbeat ran before lock connection release: in_use=%d", gotDB.Stats().InUse)
			}
			heartbeat = input
			return nil
		},
		now: func() time.Time { return now },
	}

	out := &bytes.Buffer{}
	err = runQABoundaryCommand(context.Background(), []string{
		"--qa-boundary-once", "--confirm", qaBoundaryConfirmation,
	}, out, deps)
	if err == nil || !strings.Contains(err.Error(), "advisory lock already held") {
		t.Fatalf("err=%v", err)
	}
	if heartbeat == nil || heartbeat.LastErrorAt == nil || heartbeat.LastResult == nil {
		t.Fatalf("heartbeat=%+v", heartbeat)
	}
	if !strings.Contains(*heartbeat.LastResult, "status=failed") {
		t.Fatalf("last_result=%q", *heartbeat.LastResult)
	}
	var receipt struct {
		OK      bool   `json:"ok"`
		RunID   string `json:"run_id"`
		Trigger string `json:"trigger"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.OK || receipt.RunID != "boundary-lock-run" || receipt.Trigger != "timer" || !strings.Contains(receipt.Error, "advisory lock already held") {
		t.Fatalf("receipt=%+v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenQABoundaryDBAppliesTimeoutsToEveryPooledConnection(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var gotDSN string
	opened, err := openQABoundaryDB(qaBoundaryDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone: "UTC",
				Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
			}, nil
		},
		openDB: func(_ string, dsn string) (*sql.DB, error) {
			gotDSN = dsn
			return db, nil
		},
	})
	if err != nil {
		t.Fatalf("openQABoundaryDB() err=%v", err)
	}
	if opened != db {
		t.Fatal("openQABoundaryDB() returned an unexpected database handle")
	}
	if !strings.Contains(gotDSN, "options='-c lock_timeout=100ms -c statement_timeout=120s'") {
		t.Fatalf("dsn=%q", gotDSN)
	}
}

func TestQABoundaryRequestedIncludesCutoverFinalizeCommands(t *testing.T) {
	for _, argument := range []string{
		"--qa-cutover-provision-only", "--qa-cutover-finalize-plan", "--qa-cutover-finalize",
	} {
		if !qaBoundaryRequested([]string{argument}) {
			t.Fatalf("qaBoundaryRequested(%q)=false", argument)
		}
	}
}

func TestQABoundaryCommandRejectsMultipleModes(t *testing.T) {
	for _, args := range [][]string{
		{"--qa-boundary-once", "--qa-cutover-inventory"},
		{"--qa-cutover-plan", "--qa-cutover-finalize-plan"},
		{"--qa-cutover-apply", "--qa-cutover-finalize"},
		{"--qa-cutover-provision-only", "--qa-cutover-finalize-plan"},
	} {
		err := runQABoundaryCommand(context.Background(), args, &bytes.Buffer{}, qaBoundaryDeps{})
		if err == nil || !strings.Contains(err.Error(), "exactly one mode") {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
}

func TestQABoundaryProvisionOnlyRequiresIndependentConfirmation(t *testing.T) {
	err := runQABoundaryCommand(
		context.Background(),
		[]string{"--qa-cutover-provision-only", "--confirm", "wrong"},
		&bytes.Buffer{},
		qaBoundaryDeps{},
	)
	if err == nil || !strings.Contains(err.Error(), "provision confirmation mismatch") {
		t.Fatalf("err=%v", err)
	}
}

func TestQABoundaryProvisionOnlyRunsProvisionWithoutBoundaryCleanup(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()
	provisionCalls := 0
	deps := qaBoundaryDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone: "UTC",
				Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
			}, nil
		},
		openDB:          func(string, string) (*sql.DB, error) { return db, nil },
		tryAdvisoryLock: func(context.Context, *sql.Conn) (bool, error) { return true, nil },
		unlockAdvisory:  func(context.Context, *sql.Conn) error { return nil },
		runProvisionOnly: func(context.Context, lifecycle.DB, lifecycle.Options) (lifecycle.ProvisionResult, error) {
			provisionCalls++
			return lifecycle.ProvisionResult{HoursAhead: 72, RangesRequired: 72, RangesCovered: 72}, nil
		},
		runBoundary: func(context.Context, *sql.DB, lifecycle.ControlStore, lifecycle.Options) (lifecycle.BoundaryResult, error) {
			t.Fatal("provision-only invoked full boundary cleanup")
			return lifecycle.BoundaryResult{}, nil
		},
	}
	out := &bytes.Buffer{}
	err = runQABoundaryCommand(
		context.Background(),
		[]string{
			"--qa-cutover-provision-only",
			"--confirm", qaCutoverProvisionConfirmation,
		},
		out,
		deps,
	)
	if err != nil || provisionCalls != 1 {
		t.Fatalf("runQABoundaryCommand() err=%v provisionCalls=%d", err, provisionCalls)
	}
	var result lifecycle.ProvisionResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.RangesCovered != 72 || result.RangesRequired != 72 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQABoundaryHeartbeatReportsProvisionLockRetries(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	now := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	var heartbeat *service.OpsUpsertJobHeartbeatInput
	deps := qaBoundaryDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone: "UTC",
				Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
			}, nil
		},
		openDB:          func(string, string) (*sql.DB, error) { return db, nil },
		tryAdvisoryLock: func(context.Context, *sql.Conn) (bool, error) { return true, nil },
		unlockAdvisory:  func(context.Context, *sql.Conn) error { return nil },
		runBoundary: func(context.Context, *sql.DB, lifecycle.ControlStore, lifecycle.Options) (lifecycle.BoundaryResult, error) {
			return lifecycle.BoundaryResult{Provision: lifecycle.ProvisionResult{
				HoursAhead: 72, RangesRequired: 72, RangesCovered: 72, Attempts: 3, LockRetries: 2,
			}}, nil
		},
		writeHeartbeat: func(_ context.Context, _ *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
			heartbeat = input
			return nil
		},
		now: func() time.Time { return now },
	}

	err = runQABoundaryCommand(context.Background(), []string{
		"--qa-boundary-once", "--confirm", qaBoundaryConfirmation,
	}, &bytes.Buffer{}, deps)
	if err != nil {
		t.Fatalf("runQABoundaryCommand() err=%v", err)
	}
	if heartbeat == nil || heartbeat.LastResult == nil {
		t.Fatalf("heartbeat=%+v", heartbeat)
	}
	if !strings.Contains(*heartbeat.LastResult, "provision_attempts=3 provision_lock_retries=2") {
		t.Fatalf("last_result=%q", *heartbeat.LastResult)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQABoundaryCommandRejectsCutoverPositionalArguments(t *testing.T) {
	err := runQABoundaryCommand(
		context.Background(),
		[]string{"--qa-cutover-plan", "unexpected"},
		&bytes.Buffer{},
		qaBoundaryDeps{},
	)
	if err == nil || !strings.Contains(err.Error(), "positional arguments") {
		t.Fatalf("err=%v", err)
	}
}

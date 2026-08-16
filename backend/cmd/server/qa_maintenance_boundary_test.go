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

func TestQABoundaryRequestedIncludesOnlyActiveModes(t *testing.T) {
	for _, argument := range []string{
		"--qa-boundary-once", "--qa-boundary-once=true", "--qa-boundary-once=false",
	} {
		if !qaBoundaryRequested([]string{argument}) {
			t.Fatalf("qaBoundaryRequested(%q)=false", argument)
		}
	}
	for _, argument := range []string{
		"--qa-cutover-provision-only", "--qa-cutover-provision-only=true", "--qa-cutover-provision-only=false",
		"--qa-cutover-inventory", "--qa-cutover-plan", "--qa-cutover-apply",
		"--qa-cutover-finalize-plan", "--qa-cutover-finalize",
	} {
		if qaBoundaryRequested([]string{argument}) {
			t.Fatalf("qaBoundaryRequested(%q)=true for retired mode", argument)
		}
	}
}

func TestQABoundaryCommandRejectsRetiredCutoverModes(t *testing.T) {
	for _, argument := range []string{
		"--qa-cutover-provision-only",
		"--qa-cutover-inventory", "--qa-cutover-plan", "--qa-cutover-apply",
		"--qa-cutover-finalize-plan", "--qa-cutover-finalize",
	} {
		t.Run(argument, func(t *testing.T) {
			err := runQABoundaryCommand(
				context.Background(), []string{argument}, &bytes.Buffer{}, qaBoundaryDeps{},
			)
			if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("runQABoundaryCommand(%q) err=%v", argument, err)
			}
		})
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
		openDB:            func(string, string) (*sql.DB, error) { return db, nil },
		tryAdvisoryLock:   func(context.Context, *sql.Conn) (bool, error) { return true, nil },
		unlockAdvisory:    func(context.Context, *sql.Conn) error { return nil },
		singleOwnerActive: func(context.Context, *sql.DB) (bool, error) { return false, nil },
		runTransitionBoundary: func(context.Context, *sql.DB, lifecycle.TransitionControlStore, lifecycle.Options) (lifecycle.TransitionBoundaryResult, error) {
			return lifecycle.TransitionBoundaryResult{Provision: lifecycle.ProvisionResult{
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

func TestQABoundaryRunsTransitionCleanupBeforeSingleOwnerActivation(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	transitionCalls := 0
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
		singleOwnerActive: func(context.Context, *sql.DB) (bool, error) {
			return false, nil
		},
		runTransitionBoundary: func(context.Context, *sql.DB, lifecycle.TransitionControlStore, lifecycle.Options) (lifecycle.TransitionBoundaryResult, error) {
			transitionCalls++
			return lifecycle.TransitionBoundaryResult{
				Provision:          lifecycle.ProvisionResult{HoursAhead: 72, RangesRequired: 72, RangesCovered: 72},
				DeletionAuthorized: true,
			}, nil
		},
		writeHeartbeat: func(context.Context, *sql.DB, *service.OpsUpsertJobHeartbeatInput) error { return nil },
	}
	out := &bytes.Buffer{}
	err = runQABoundaryCommand(context.Background(), []string{
		"--qa-boundary-once", "--confirm", qaBoundaryConfirmation,
	}, out, deps)
	if err != nil {
		t.Fatalf("runQABoundaryCommand() err=%v", err)
	}
	if transitionCalls != 1 {
		t.Fatalf("transitionCalls=%d", transitionCalls)
	}
	var receipt struct {
		DeletionAuthorized bool `json:"deletion_authorized"`
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.DeletionAuthorized {
		t.Fatalf("receipt=%s", out.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQABoundaryCommandRefusesAfterSingleOwnerActivation(t *testing.T) {
	transitionCalls := 0
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()
	err = runQABoundaryCommand(
		context.Background(),
		[]string{"--qa-boundary-once", "--confirm", qaBoundaryConfirmation},
		&bytes.Buffer{},
		qaBoundaryDeps{
			loadConfig: func() (*config.Config, error) {
				return &config.Config{
					Timezone: "UTC",
					Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
				}, nil
			},
			openDB:            func(string, string) (*sql.DB, error) { return db, nil },
			tryAdvisoryLock:   func(context.Context, *sql.Conn) (bool, error) { return true, nil },
			unlockAdvisory:    func(context.Context, *sql.Conn) error { return nil },
			singleOwnerActive: func(context.Context, *sql.DB) (bool, error) { return true, nil },
			runTransitionBoundary: func(context.Context, *sql.DB, lifecycle.TransitionControlStore, lifecycle.Options) (lifecycle.TransitionBoundaryResult, error) {
				transitionCalls++
				return lifecycle.TransitionBoundaryResult{}, nil
			},
			writeHeartbeat: func(context.Context, *sql.DB, *service.OpsUpsertJobHeartbeatInput) error { return nil },
		},
	)
	if err == nil || !strings.Contains(err.Error(), "single-owner") {
		t.Fatalf("err=%v", err)
	}
	if transitionCalls != 0 {
		t.Fatalf("transitionCalls=%d", transitionCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQABoundaryCommandRejectsCutoverPositionalArguments(t *testing.T) {
	err := runQABoundaryCommand(
		context.Background(),
		[]string{"--qa-boundary-once", "unexpected"},
		&bytes.Buffer{},
		qaBoundaryDeps{},
	)
	if err == nil || !strings.Contains(err.Error(), "positional arguments") {
		t.Fatalf("err=%v", err)
	}
}

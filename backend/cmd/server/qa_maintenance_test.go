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
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestQAMaintenanceRejectsWrongConfirmationBeforeDependencies(t *testing.T) {
	loadCalled := false
	openCalled := false
	deps := qaMaintenanceDeps{
		loadConfig: func() (*config.Config, error) {
			loadCalled = true
			return nil, nil
		},
		openDB: func(string, string) (*sql.DB, error) {
			openCalled = true
			return nil, nil
		},
	}

	err := runQAMaintenanceCommand(
		context.Background(),
		[]string{"--qa-maintenance-once", "--confirm", "wrong"},
		&bytes.Buffer{},
		deps,
	)
	if err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	if loadCalled || openCalled {
		t.Fatalf("wrong confirmation touched dependencies: load=%v open=%v", loadCalled, openCalled)
	}
}

func TestDefaultQAMaintenancePlanShardUpsertsControlRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New()=%v", err)
	}
	defer func() { _ = db.Close() }()

	windowStart := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT COUNT\\(\\*\\)").
		WithArgs(windowStart, windowEnd).
		WillReturnRows(sqlmock.NewRows([]string{"count", "blob_ref_count"}).AddRow(42, 7))
	mock.ExpectExec("INSERT INTO qa_archive_shards").
		WithArgs(windowStart, windowEnd, "pending", int64(42), int64(7), "raw/v1/date=2026-08-06/hour=09", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn()=%v", err)
	}
	defer func() { _ = conn.Close() }()

	plan, err := defaultQAMaintenancePlanShard(
		context.Background(),
		conn,
		windowStart,
		windowEnd,
		"raw/v1/date=2026-08-06/hour=09",
		false,
		15,
	)
	if err != nil {
		t.Fatalf("defaultQAMaintenancePlanShard()=%v", err)
	}
	if plan.RecordCount != 42 || plan.BlobRefCount != 7 || plan.ArchiveEnabled {
		t.Fatalf("plan=%+v", plan)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestQAMaintenanceSuccessUsesArchiveOnlyPath(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New()=%v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectPing()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT pg_try_advisory_lock").
		WithArgs(qaMaintenanceAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("SELECT pg_advisory_unlock").
		WithArgs(qaMaintenanceAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	fixedNow := time.Date(2026, 8, 6, 10, 15, 0, 0, time.UTC)
	windowStart := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	loadCalls := 0
	openCalls := 0
	planCalls := 0
	var heartbeat *service.OpsUpsertJobHeartbeatInput
	deps := qaMaintenanceDeps{
		loadConfig: func() (*config.Config, error) {
			loadCalls++
			return &config.Config{
				Timezone: "UTC",
				Database: config.DatabaseConfig{
					Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable",
				},
				QaArchive: config.QaArchiveConfig{Enabled: false, SealDelayMinutes: 15},
			}, nil
		},
		openDB: func(driverName, dsn string) (*sql.DB, error) {
			openCalls++
			if driverName != "postgres" || !strings.Contains(dsn, "TimeZone=UTC") {
				t.Fatalf("unexpected open args: driver=%q dsn=%q", driverName, dsn)
			}
			return db, nil
		},
		planShard: func(_ context.Context, _ *sql.Conn, ws, we time.Time, s3Prefix string, archiveEnabled bool, sealDelay int) (qaMaintenancePlan, error) {
			planCalls++
			if !ws.Equal(windowStart) || !we.Equal(windowEnd) || s3Prefix != "raw/v1/date=2026-08-06/hour=09" || archiveEnabled || sealDelay != 15 {
				t.Fatalf("unexpected planShard args: ws=%s we=%s prefix=%q", ws, we, s3Prefix)
			}
			return qaMaintenancePlan{
				WindowStart: windowStart, WindowEnd: windowEnd, S3Prefix: s3Prefix,
				RecordCount: 42, BlobRefCount: 7, ArchiveEnabled: archiveEnabled,
			}, nil
		},
		writeHeartbeat: func(heartbeatCtx context.Context, gotDB *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
			if gotDB != db {
				t.Fatalf("heartbeat db=%p want %p", gotDB, db)
			}
			deadline, ok := heartbeatCtx.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining <= 0 || remaining > qaMaintenanceHeartbeatTimeout {
				t.Fatalf("heartbeat context must have a live bounded deadline: ok=%v remaining=%s", ok, remaining)
			}
			heartbeat = input
			return nil
		},
		now: func() time.Time { return fixedNow },
	}

	out := &bytes.Buffer{}
	err = runQAMaintenanceCommand(
		context.Background(),
		[]string{"--qa-maintenance-once", "--confirm", qaMaintenanceConfirmation},
		out,
		deps,
	)
	if err != nil {
		t.Fatalf("runQAMaintenanceCommand()=%v", err)
	}
	if loadCalls != 1 || openCalls != 1 || planCalls != 1 {
		t.Fatalf("calls load=%d open=%d plan=%d", loadCalls, openCalls, planCalls)
	}
	if heartbeat == nil || heartbeat.JobName != qaMaintenanceJobName || heartbeat.LastSuccessAt == nil {
		t.Fatalf("invalid heartbeat: %+v", heartbeat)
	}
	if heartbeat.LastResult == nil ||
		!strings.Contains(*heartbeat.LastResult, "deletion_authorized=false") ||
		!strings.Contains(*heartbeat.LastResult, "upload_authorized=false") {
		t.Fatalf("heartbeat result must deny deletion/upload: %+v", heartbeat.LastResult)
	}

	var receipt struct {
		ReceiptVersion     int    `json:"receipt_version"`
		Mode               string `json:"mode"`
		UploadAuthorized   bool   `json:"upload_authorized"`
		DeletionAuthorized bool   `json:"deletion_authorized"`
		Plan               struct {
			RecordCount int64 `json:"record_count"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatalf("decode output %q: %v", out.String(), err)
	}
	if receipt.ReceiptVersion != qaMaintenanceReceiptVersion ||
		receipt.Mode != qaMaintenanceReceiptMode ||
		receipt.UploadAuthorized ||
		receipt.DeletionAuthorized ||
		receipt.Plan.RecordCount != 42 {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestQAMaintenanceUploadPathWhenArchiveEnabled(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New()=%v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectPing()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("UPDATE qa_archive_shards SET state = \\$1, updated_at = \\$2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	windowStart := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	uploadCalled := false
	deps := qaMaintenanceDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone: "UTC",
				Database: config.DatabaseConfig{
					Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable",
				},
				QaArchive: config.QaArchiveConfig{
					Enabled:          true,
					SealDelayMinutes: 15,
					Storage: config.QACaptureStorageConfig{
						Driver: "s3", Region: "us-east-1", Bucket: "b", Prefix: "raw/v1",
					},
				},
			}, nil
		},
		openDB: func(string, string) (*sql.DB, error) { return db, nil },
		newObjectStore: func(context.Context, config.QACaptureStorageConfig) (archive.ObjectStore, error) {
			return archive.NewMemoryObjectStore(), nil
		},
		planShard: func(context.Context, *sql.Conn, time.Time, time.Time, string, bool, int) (qaMaintenancePlan, error) {
			return qaMaintenancePlan{
				WindowStart: windowStart, WindowEnd: windowEnd, RecordCount: 1, ArchiveEnabled: true,
			}, nil
		},
		uploadShard: func(_ context.Context, _ *sql.Conn, _ archive.ObjectStore, plan qaMaintenancePlan, _ string) (archive.UploadResult, error) {
			uploadCalled = true
			return archive.UploadResult{SegmentID: "seg-1", CommitKey: "date=2026-08-06/hour=09/commit.json"}, nil
		},
		writeHeartbeat: func(context.Context, *sql.DB, *service.OpsUpsertJobHeartbeatInput) error { return nil },
		now:            func() time.Time { return windowEnd.Add(15 * time.Minute) },
	}

	out := &bytes.Buffer{}
	if err := runQAMaintenanceCommand(
		context.Background(),
		[]string{"--qa-maintenance-once", "--confirm", qaMaintenanceConfirmation},
		out,
		deps,
	); err != nil {
		t.Fatalf("runQAMaintenanceCommand()=%v", err)
	}
	if !uploadCalled {
		t.Fatal("expected uploadShard to run")
	}
	var receipt struct {
		Mode             string `json:"mode"`
		UploadAuthorized bool   `json:"upload_authorized"`
		Plan             struct {
			Uploaded  bool   `json:"uploaded"`
			SegmentID string `json:"segment_id"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt.Mode != qaMaintenanceReceiptModeUpload || !receipt.UploadAuthorized || !receipt.Plan.Uploaded || receipt.Plan.SegmentID != "seg-1" {
		t.Fatalf("receipt=%+v", receipt)
	}
}

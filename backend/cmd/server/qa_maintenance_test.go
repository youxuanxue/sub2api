//go:build unit

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/lifecycle"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func qaMaintenanceTestProvision(context.Context, *sql.Conn) (lifecycle.ProvisionResult, error) {
	return lifecycle.ProvisionResult{RangesCovered: lifecycle.HourlyHorizon, RangesRequired: lifecycle.HourlyHorizon}, nil
}

func qaMaintenanceTestInactive(context.Context, *sql.Conn) (bool, error) {
	return false, nil
}

func TestQAMaintenanceDropPhaseDoesNothingBeforeSingleOwnerActivation(t *testing.T) {
	dropCalls := 0
	result, err := runQAMaintenanceDropPhase(
		context.Background(),
		qaMaintenancePlan{WindowStart: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC), State: archive.StateCommitted, RestoreVerified: true},
		nil,
		qaMaintenanceDropPhaseDeps{
			active: func(context.Context) (bool, error) { return false, nil },
			drop: func(context.Context, archive.Window) (lifecycle.ExpiryResult, error) {
				dropCalls++
				return lifecycle.ExpiryResult{}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Active || result.DeletionAuthorized || dropCalls != 0 {
		t.Fatalf("result=%+v dropCalls=%d", result, dropCalls)
	}
}

func TestQAMaintenanceDropPhaseDropsNormalThenCompensation(t *testing.T) {
	normalStart := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	compensationStart := normalStart.Add(-time.Hour)
	var dropped []time.Time
	resumeCalls := 0
	result, err := runQAMaintenanceDropPhase(
		context.Background(),
		qaMaintenancePlan{WindowStart: normalStart, WindowEnd: normalStart.Add(time.Hour), State: archive.StateCommitted, RestoreVerified: true},
		&qaMaintenancePlan{WindowStart: compensationStart, WindowEnd: compensationStart.Add(time.Hour), State: archive.StateCommitted, RestoreVerified: true},
		qaMaintenanceDropPhaseDeps{
			active: func(context.Context) (bool, error) { return true, nil },
			drop: func(_ context.Context, window archive.Window) (lifecycle.ExpiryResult, error) {
				dropped = append(dropped, window.Start)
				return lifecycle.ExpiryResult{PartitionName: "qa_hour", SourceDroppedAt: window.End}, nil
			},
			resume: func(context.Context) ([]lifecycle.HotCleanupResult, error) {
				resumeCalls++
				return []lifecycle.HotCleanupResult{{ShardID: 9, WindowStart: compensationStart.Add(-time.Hour), Cleaned: true}}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Active || !result.DeletionAuthorized || result.Normal == nil || result.Compensation == nil || len(result.CleanupResumed) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if resumeCalls != 1 {
		t.Fatalf("resumeCalls=%d", resumeCalls)
	}
	if len(dropped) != 2 || !dropped[0].Equal(normalStart) || !dropped[1].Equal(compensationStart) {
		t.Fatalf("dropped=%v", dropped)
	}
}

func TestQAMaintenanceDropPhaseStopsWhenNormalDropFails(t *testing.T) {
	dropCalls := 0
	normalStart := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	_, err := runQAMaintenanceDropPhase(
		context.Background(),
		qaMaintenancePlan{WindowStart: normalStart, WindowEnd: normalStart.Add(time.Hour), State: archive.StateCommitted, RestoreVerified: true},
		&qaMaintenancePlan{WindowStart: normalStart.Add(-time.Hour), WindowEnd: normalStart, State: archive.StateCommitted, RestoreVerified: true},
		qaMaintenanceDropPhaseDeps{
			active: func(context.Context) (bool, error) { return true, nil },
			drop: func(context.Context, archive.Window) (lifecycle.ExpiryResult, error) {
				dropCalls++
				return lifecycle.ExpiryResult{}, errors.New("seal changed")
			},
			resume: func(context.Context) ([]lifecycle.HotCleanupResult, error) {
				t.Fatal("cleanup resume must not run after normal DROP failure")
				return nil, nil
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "normal") || dropCalls != 1 {
		t.Fatalf("err=%v dropCalls=%d", err, dropCalls)
	}
}

func TestQAMaintenanceDropPhasePreservesCommittedNormalDropOnCleanupError(t *testing.T) {
	normalStart := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	result, err := runQAMaintenanceDropPhase(
		context.Background(),
		qaMaintenancePlan{WindowStart: normalStart, WindowEnd: normalStart.Add(time.Hour), State: archive.StateCommitted, RestoreVerified: true},
		nil,
		qaMaintenanceDropPhaseDeps{
			active: func(context.Context) (bool, error) { return true, nil },
			drop: func(context.Context, archive.Window) (lifecycle.ExpiryResult, error) {
				return lifecycle.ExpiryResult{
					PartitionName: "qa_records_20260815_09", SourceDroppedAt: normalStart.Add(time.Hour),
				}, errors.New("hot cleanup unavailable")
			},
			resume: func(context.Context) ([]lifecycle.HotCleanupResult, error) {
				t.Fatal("cleanup resume must not run after the normal drop call returned an error")
				return nil, nil
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "hot cleanup unavailable") {
		t.Fatalf("err=%v", err)
	}
	if !result.DeletionAuthorized || result.Normal == nil || result.Normal.PartitionName != "qa_records_20260815_09" || result.Normal.SourceDroppedAt.IsZero() {
		t.Fatalf("result lost committed DROP facts: %+v", result)
	}
}

func TestQAMaintenanceDropArchivedHourResumesCleanupAfterCommittedDrop(t *testing.T) {
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

	hour := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	droppedAt := hour.Add(75 * time.Minute)
	dataRoot := t.TempDir()
	t.Setenv("DATA_DIR", dataRoot)
	for _, rel := range []string{
		"qa_blobs/2026/08/15/09/re/request.json.zst",
		"qa_dlq/2026/08/15/09/failed.json.zst",
	} {
		path := filepath.Join(dataRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	mock.ExpectQuery("FROM pg_inherits").
		WithArgs(lifecycle.TableQARecords).
		WillReturnRows(sqlmock.NewRows([]string{
			"nspname", "relname", "bound_expr", "lower_unbounded", "upper_unbounded", "is_default", "lower_bound", "upper_bound",
		}))
	mock.ExpectQuery("SELECT id, source_partition_name, source_dropped_at, hot_files_cleaned_at").
		WithArgs(hour).
		WillReturnRows(sqlmock.NewRows([]string{"id", "source_partition_name", "source_dropped_at", "hot_files_cleaned_at"}).
			AddRow(int64(44), "qa_records_20260815_09", droppedAt, nil))
	mock.ExpectExec("UPDATE qa_archive_shards SET").
		WithArgs(int64(44), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := defaultQAMaintenanceDropArchivedHour(
		context.Background(),
		conn,
		archive.Window{Start: hour, End: hour.Add(time.Hour)},
		hour.Add(2*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.SourceDroppedAt.Equal(droppedAt) || !result.HotFilesCleaned {
		t.Fatalf("result=%+v", result)
	}
	for _, dir := range []string{
		"qa_blobs/2026/08/15/09",
		"qa_dlq/2026/08/15/09",
	} {
		if _, err := os.Stat(filepath.Join(dataRoot, filepath.FromSlash(dir))); !os.IsNotExist(err) {
			t.Fatalf("cleanup left %s: %v", dir, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

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
		planShard: func(_ context.Context, _ *sql.Conn, ws, we time.Time, s3Prefix string, archiveEnabled bool) (qaMaintenancePlan, error) {
			planCalls++
			if !ws.Equal(windowStart) || !we.Equal(windowEnd) || s3Prefix != "raw/v1/date=2026-08-06/hour=09" || archiveEnabled {
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
		provision:         qaMaintenanceTestProvision,
		singleOwnerActive: qaMaintenanceTestInactive,
		now:               func() time.Time { return fixedNow },
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
		!strings.Contains(*heartbeat.LastResult, "status=planned") ||
		!strings.Contains(*heartbeat.LastResult, "normal_state=pending") ||
		strings.Contains(*heartbeat.LastResult, "status=committed") ||
		!strings.Contains(*heartbeat.LastResult, "normal_restore_verified=false") ||
		!strings.Contains(*heartbeat.LastResult, "deletion_authorized=false") ||
		!strings.Contains(*heartbeat.LastResult, "upload_authorized=false") {
		t.Fatalf("heartbeat result must report a no-write plan and deny deletion/upload: %+v", heartbeat.LastResult)
	}

	var receipt struct {
		ReceiptVersion     int    `json:"receipt_version"`
		Mode               string `json:"mode"`
		UploadAuthorized   bool   `json:"upload_authorized"`
		DeletionAuthorized bool   `json:"deletion_authorized"`
		Plan               struct {
			RecordCount     int64  `json:"record_count"`
			State           string `json:"state"`
			RestoreVerified bool   `json:"restore_verified"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatalf("decode output %q: %v", out.String(), err)
	}
	if receipt.ReceiptVersion != qaMaintenanceReceiptVersion ||
		receipt.Mode != "qa_single_owner_lifecycle" ||
		receipt.UploadAuthorized ||
		receipt.DeletionAuthorized ||
		receipt.Plan.RecordCount != 42 ||
		receipt.Plan.State != archive.StatePending ||
		receipt.Plan.RestoreVerified {
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
		reconcileShard: func(_ context.Context, _ *sql.Conn, _ archive.ObjectStore, window archive.Window, _, _ string) (archive.ReconcileReceipt, error) {
			uploadCalled = true
			if !window.Start.Equal(windowStart) || !window.End.Equal(windowEnd) {
				t.Fatalf("reconcile window=%+v", window)
			}
			return archive.ReconcileReceipt{
				WindowStart: windowStart, WindowEnd: windowEnd, Uploaded: true,
				CommitKey: "date=2026-08-06/hour=09/commit.json", CommitETag: "etag-v2",
				SegmentCount: 2, RecordCount: 42, BlobRefCount: 7,
			}, nil
		},
		selectOldest: func(_ context.Context, _ *sql.Conn, normal archive.Window, cutoff time.Time) (archive.CatchupSelection, bool, error) {
			if normal.Start != windowStart || normal.End != windowEnd || !cutoff.IsZero() {
				t.Fatalf("select normal=%+v cutoff=%s", normal, cutoff)
			}
			return archive.CatchupSelection{}, false, nil
		},
		writeHeartbeat:    func(context.Context, *sql.DB, *service.OpsUpsertJobHeartbeatInput) error { return nil },
		provision:         qaMaintenanceTestProvision,
		singleOwnerActive: qaMaintenanceTestInactive,
		now:               func() time.Time { return windowEnd.Add(15 * time.Minute) },
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
		t.Fatal("expected reconcileShard to run")
	}
	var receipt struct {
		Mode             string `json:"mode"`
		UploadAuthorized bool   `json:"upload_authorized"`
		Plan             struct {
			Uploaded     bool   `json:"uploaded"`
			SegmentCount int    `json:"segment_count"`
			RecordCount  int64  `json:"record_count"`
			CommitETag   string `json:"commit_etag"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt.Mode != "qa_single_owner_lifecycle" || !receipt.UploadAuthorized || !receipt.Plan.Uploaded ||
		receipt.Plan.SegmentCount != 2 || receipt.Plan.RecordCount != 42 || receipt.Plan.CommitETag != "etag-v2" {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestQAMaintenanceReconcileFailureWritesFailureHeartbeat(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("SELECT pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	var heartbeat *service.OpsUpsertJobHeartbeatInput
	fixedNow := time.Date(2026, 8, 7, 2, 15, 0, 0, time.UTC)
	deps := qaMaintenanceDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone: "UTC",
				Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
				QaArchive: config.QaArchiveConfig{
					Enabled: true, SealDelayMinutes: 15,
					Storage: config.QACaptureStorageConfig{Driver: "s3", Region: "us-east-1", Bucket: "b"},
				},
			}, nil
		},
		openDB: func(string, string) (*sql.DB, error) { return db, nil },
		newObjectStore: func(context.Context, config.QACaptureStorageConfig) (archive.ObjectStore, error) {
			return archive.NewMemoryObjectStore(), nil
		},
		reconcileShard: func(context.Context, *sql.Conn, archive.ObjectStore, archive.Window, string, string) (archive.ReconcileReceipt, error) {
			return archive.ReconcileReceipt{}, &archive.IntegrityError{Code: archive.IntegrityCorruptArtifact, Err: errors.New("checksum mismatch")}
		},
		writeHeartbeat: func(_ context.Context, _ *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
			heartbeat = input
			return nil
		},
		provision: qaMaintenanceTestProvision,
		now:       func() time.Time { return fixedNow },
	}

	err = runQAMaintenanceCommand(context.Background(), []string{
		"--qa-maintenance-once", "--confirm", qaMaintenanceConfirmation,
	}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("runQAMaintenanceCommand() error=%v", err)
	}
	if heartbeat == nil || heartbeat.LastErrorAt == nil || heartbeat.LastSuccessAt != nil ||
		heartbeat.LastResult == nil || !strings.Contains(*heartbeat.LastResult, "deletion_authorized=false") {
		t.Fatalf("failure heartbeat=%+v", heartbeat)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQAMaintenanceRejectsRemovedBackfillFlagBeforeDependencies(t *testing.T) {
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
		[]string{"--qa-maintenance-once", "--qa-maintenance-backfill-once", "--confirm", qaMaintenanceConfirmation},
		&bytes.Buffer{},
		deps,
	)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("runQAMaintenanceCommand() error=%v", err)
	}
	if loadCalled || openCalled {
		t.Fatalf("retired backfill touched dependencies: load=%v open=%v", loadCalled, openCalled)
	}
}

func TestQAMaintenanceLockContentionWritesFailureWithoutUnlock(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	mock.ExpectClose()

	var heartbeat *service.OpsUpsertJobHeartbeatInput
	fixedNow := time.Date(2026, 8, 7, 2, 15, 0, 0, time.UTC)
	deps := qaMaintenanceDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone:  "UTC",
				Database:  config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
				QaArchive: config.QaArchiveConfig{Enabled: true, SealDelayMinutes: 15},
			}, nil
		},
		openDB: func(string, string) (*sql.DB, error) { return db, nil },
		writeHeartbeat: func(_ context.Context, _ *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
			heartbeat = input
			return nil
		},
		now: func() time.Time { return fixedNow },
	}

	err = runQAMaintenanceCommand(
		context.Background(),
		[]string{"--qa-maintenance-once", "--confirm", qaMaintenanceConfirmation},
		&bytes.Buffer{}, deps,
	)
	if err == nil || !strings.Contains(err.Error(), "already held") {
		t.Fatalf("runQAMaintenanceCommand() error=%v", err)
	}
	if heartbeat == nil || heartbeat.LastErrorAt == nil || heartbeat.LastSuccessAt != nil || heartbeat.LastError == nil {
		t.Fatalf("failure heartbeat=%+v", heartbeat)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

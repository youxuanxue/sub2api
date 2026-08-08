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
)

var testWindow = time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)

func TestUS045_SetForwardCutoverRejectsGenericWindowMoveAndUnsetBeforeDependencies(t *testing.T) {
	called := false
	deps := cliDeps{loadConfig: func() (*config.Config, error) {
		called = true
		return nil, errors.New("must not run")
	}}
	approved := archive.Phase2ForwardCutoverWindow()
	confirm := forwardCutoverConfirmationPrefix + ":" + approved.Start.Format(time.RFC3339)
	for _, args := range [][]string{
		{"set-forward-cutover", "--window-start", approved.Start.Format(time.RFC3339), "--confirm", confirm},
		{"move-forward-cutover"},
		{"unset-forward-cutover"},
	} {
		err := runCLI(context.Background(), args, &bytes.Buffer{}, deps)
		if err == nil {
			t.Fatalf("runCLI(%v) unexpectedly succeeded", args)
		}
	}
	if called {
		t.Fatal("dependencies ran for a forbidden cutover surface")
	}
}

func TestUS045_SetForwardCutoverUsesFixedApprovedWindow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	mock.ExpectClose()
	approved := archive.Phase2ForwardCutoverWindow()
	called := false
	deps := cliDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{Timezone: "UTC", Database: config.DatabaseConfig{
				Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable",
			}}, nil
		},
		openDB: func(string, string) (*sql.DB, error) { return db, nil },
		setForwardCutover: func(context.Context, *sql.Conn) (archive.ForwardCutover, error) {
			called = true
			return archive.ForwardCutover{ShardID: 45, Window: approved}, nil
		},
	}
	confirm := forwardCutoverConfirmationPrefix + ":" + approved.Start.Format(time.RFC3339)
	out := &bytes.Buffer{}
	if err := runCLI(context.Background(), []string{
		"set-forward-cutover", "--confirm", confirm,
	}, out, deps); err != nil {
		t.Fatalf("runCLI()=%v", err)
	}
	if !called {
		t.Fatal("fixed cutover store operation was not called")
	}
	var receipt map[string]any
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["command"] != "set-forward-cutover" || receipt["window_start"] != approved.Start.Format(time.RFC3339) ||
		receipt["forward_cutover"] != true || receipt["deletion_authorized"] != false {
		t.Fatalf("receipt=%v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUS045_SetForwardCutoverRequiresExactConfirmationBeforeDependencies(t *testing.T) {
	called := false
	deps := cliDeps{loadConfig: func() (*config.Config, error) {
		called = true
		return nil, errors.New("must not run")
	}}
	err := runCLI(context.Background(), []string{
		"set-forward-cutover", "--confirm", forwardCutoverConfirmationPrefix,
	}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "exact cutover confirmation") {
		t.Fatalf("runCLI() error=%v", err)
	}
	if called {
		t.Fatal("dependencies ran before exact cutover confirmation validation")
	}
}

func TestRestoreRejectsOutputOutsideIsolatedRootBeforeDependencies(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	called := false
	deps := cliDeps{loadConfig: func() (*config.Config, error) {
		called = true
		return nil, errors.New("must not run")
	}}
	confirm := restoreConfirmationPrefix + ":" + testWindow.Format(time.RFC3339)
	err := runCLI(context.Background(), []string{
		"restore", "--window-start", testWindow.Format(time.RFC3339),
		"--output", filepath.Join(t.TempDir(), "escaped"), "--confirm", confirm,
	}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "isolated restore root") {
		t.Fatalf("runCLI() error=%v", err)
	}
	if called {
		t.Fatal("dependencies ran before restore path validation")
	}
}

func TestRepairApplyRejectsUnboundConfirmationBeforeDependencies(t *testing.T) {
	called := false
	deps := cliDeps{loadConfig: func() (*config.Config, error) {
		called = true
		return nil, errors.New("must not run")
	}}
	err := runCLI(context.Background(), []string{
		"repair-apply", "--window-start", testWindow.Format(time.RFC3339),
		"--confirm", repairConfirmationPrefix,
	}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "window-bound confirmation") {
		t.Fatalf("runCLI() error=%v", err)
	}
	if called {
		t.Fatal("dependencies ran before confirmation validation")
	}
}

func TestRepairApplyRequiresControllerProofBeforeDependencies(t *testing.T) {
	called := false
	deps := cliDeps{
		loadConfig: func() (*config.Config, error) {
			called = true
			return nil, errors.New("must not run")
		},
		readSafetyProof: func() ([]byte, error) { return nil, os.ErrNotExist },
	}
	confirm := repairConfirmationPrefix + ":" + testWindow.Format(time.RFC3339)
	err := runCLI(context.Background(), []string{
		"repair-apply", "--window-start", testWindow.Format(time.RFC3339), "--confirm", confirm,
	}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "controller safety proof") {
		t.Fatalf("runCLI() error=%v", err)
	}
	if called {
		t.Fatal("dependencies ran before controller proof validation")
	}
}

func TestRestoreRequiresWindowBoundPrivacyConfirmation(t *testing.T) {
	called := false
	deps := cliDeps{loadConfig: func() (*config.Config, error) {
		called = true
		return nil, errors.New("must not run")
	}}
	err := runCLI(context.Background(), []string{
		"restore", "--window-start", testWindow.Format(time.RFC3339),
		"--output", t.TempDir(), "--confirm", restoreConfirmationPrefix,
	}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "privacy confirmation") {
		t.Fatalf("runCLI() error=%v", err)
	}
	if called {
		t.Fatal("dependencies ran before privacy confirmation validation")
	}
}

func TestRepairApplyRefusesInactiveCleanupHold(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	mock.ExpectClose()
	deps := repairTestDeps(db)
	deps.cleanupHoldActive = func(context.Context, *sql.DB) (bool, error) { return false, nil }
	confirm := repairConfirmationPrefix + ":" + testWindow.Format(time.RFC3339)
	err = runCLI(context.Background(), []string{
		"repair-apply", "--window-start", testWindow.Format(time.RFC3339), "--confirm", confirm,
	}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "cleanup hold is not active") {
		t.Fatalf("runCLI() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepairApplyRefusesMaintenanceLockContentionWithoutReconcile(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(archive.MaintenanceAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	mock.ExpectClose()
	deps := repairTestDeps(db)
	deps.reconcile = func(context.Context, *sql.Conn, archive.ObjectStore, archive.Window, string, string) (archive.ReconcileReceipt, error) {
		t.Fatal("reconcile called without lock")
		return archive.ReconcileReceipt{}, nil
	}
	confirm := repairConfirmationPrefix + ":" + testWindow.Format(time.RFC3339)
	err = runCLI(context.Background(), []string{
		"repair-apply", "--window-start", testWindow.Format(time.RFC3339), "--confirm", confirm,
	}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "maintenance lock already held") {
		t.Fatalf("runCLI() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepairApplyCallsReconcilerAndDeniesDeletion(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(archive.MaintenanceAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("SELECT pg_advisory_unlock").WithArgs(archive.MaintenanceAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectClose()
	deps := repairTestDeps(db)
	called := false
	deps.reconcile = func(_ context.Context, _ *sql.Conn, _ archive.ObjectStore, window archive.Window, _, _ string) (archive.ReconcileReceipt, error) {
		called = true
		return archive.ReconcileReceipt{
			WindowStart: window.Start, WindowEnd: window.End, CommitKey: archive.ShardRelativePrefix(window.Start) + "/commit.json",
			CommitETag: "etag-after", SegmentCount: 2, RecordCount: 884, DeletionAuthorized: false,
		}, nil
	}
	deps.verifyCommit = func(context.Context, archive.ObjectStore, string, string) (archive.VerifiedCommit, error) {
		return archive.VerifiedCommit{
			Document: archive.CommitDocument{
				WindowStart: testWindow, WindowEnd: testWindow.Add(time.Hour),
				Segments: []archive.CommitSegment{{SegmentID: "base-1", SegmentKind: archive.SegmentKindBase, ManifestKey: "base/manifest.json", ManifestSHA256: "sha-base"}, {SegmentID: "delta-1", SegmentKind: archive.SegmentKindDelta, ManifestKey: "delta/manifest.json", ManifestSHA256: "sha-delta"}},
			},
			ETag: "etag-after", RecordCount: 884,
		}, nil
	}
	confirm := repairConfirmationPrefix + ":" + testWindow.Format(time.RFC3339)
	out := &bytes.Buffer{}
	if err := runCLI(context.Background(), []string{
		"repair-apply", "--window-start", testWindow.Format(time.RFC3339), "--confirm", confirm,
	}, out, deps); err != nil {
		t.Fatalf("runCLI()=%v", err)
	}
	if !called {
		t.Fatal("reconciler was not called")
	}
	var receipt map[string]any
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	manifests, ok := receipt["manifests"].([]any)
	if receipt["command"] != "repair-apply" || receipt["deletion_authorized"] != false || receipt["cleanup_hold_active"] != true || !ok || len(manifests) != 2 {
		t.Fatalf("receipt=%v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepairPlanReportsControlMismatch(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	mock.ExpectClose()
	store := archive.NewMemoryObjectStore()
	commitKey := archive.ShardRelativePrefix(testWindow) + "/commit.json"
	if err := store.Put(context.Background(), commitKey, []byte("commit"), "application/json"); err != nil {
		t.Fatal(err)
	}
	deps := cliDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone: "UTC", Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
				QaArchive: config.QaArchiveConfig{Storage: testStorage()},
			}, nil
		},
		openDB:         func(string, string) (*sql.DB, error) { return db, nil },
		newObjectStore: func(context.Context, config.QACaptureStorageConfig) (archive.ObjectStore, error) { return store, nil },
		verifyCommit: func(context.Context, archive.ObjectStore, string, string) (archive.VerifiedCommit, error) {
			return archive.VerifiedCommit{Document: archive.CommitDocument{WindowStart: testWindow, WindowEnd: testWindow.Add(time.Hour)}, ETag: "etag-s3", RecordCount: 407}, nil
		},
		inspectControl: func(context.Context, *sql.Conn, archive.Window) (controlStatus, error) {
			return controlStatus{Exists: true, State: archive.StateCommitted, CommitETag: "etag-db", AggregateRecordCount: 406}, nil
		},
		planSourceDelta: func(context.Context, *sql.Conn, archive.Window, archive.VerifiedCommit) (archive.SourceDeltaPlan, error) {
			return archive.SourceDeltaPlan{SourceRecordCount: 884, CommittedRecordCount: 407, DeltaRecordCount: 477}, nil
		},
	}
	out := &bytes.Buffer{}
	if err := runCLI(context.Background(), []string{"repair-plan", "--window-start", testWindow.Format(time.RFC3339)}, out, deps); err != nil {
		t.Fatalf("runCLI()=%v", err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["control_mismatch"] != true || receipt["delta_record_count"] != float64(477) ||
		receipt["cleanup_eligible"] != false || receipt["deletion_authorized"] != false {
		t.Fatalf("receipt=%v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectReportsBlockedIntegrityFailureWithoutWriting(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()
	mock.ExpectClose()
	deps := cliDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone: "UTC", Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
				QaArchive: config.QaArchiveConfig{Storage: testStorage()},
			}, nil
		},
		openDB: func(string, string) (*sql.DB, error) { return db, nil },
		newObjectStore: func(context.Context, config.QACaptureStorageConfig) (archive.ObjectStore, error) {
			return archive.NewMemoryObjectStore(), nil
		},
		inspectControl: func(context.Context, *sql.Conn, archive.Window) (controlStatus, error) {
			return controlStatus{Exists: true, State: archive.StateFailed, VerificationErrorCode: archive.IntegrityMissingEvidence, CleanupEligible: false}, nil
		},
		verifyCommit: func(context.Context, archive.ObjectStore, string, string) (archive.VerifiedCommit, error) {
			return archive.VerifiedCommit{}, &archive.IntegrityError{Code: archive.IntegrityMissingEvidence, RequestID: "safe-request-id", Err: errors.New("file not found")}
		},
	}
	out := &bytes.Buffer{}
	if err := runCLI(context.Background(), []string{"inspect", "--window-start", testWindow.Format(time.RFC3339)}, out, deps); err != nil {
		t.Fatalf("runCLI()=%v", err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["verified"] != false || receipt["blocked"] != true || receipt["verification_error_code"] != archive.IntegrityMissingEvidence || receipt["deletion_authorized"] != false {
		t.Fatalf("receipt=%v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyUsesReadOnlyVerifierAndDeniesDeletion(t *testing.T) {
	deps := cliDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{QaArchive: config.QaArchiveConfig{Storage: testStorage()}}, nil
		},
		newObjectStore: func(context.Context, config.QACaptureStorageConfig) (archive.ObjectStore, error) {
			return archive.NewMemoryObjectStore(), nil
		},
		verifyCommit: func(_ context.Context, _ archive.ObjectStore, key, restoreDir string) (archive.VerifiedCommit, error) {
			if key != archive.ShardRelativePrefix(testWindow)+"/commit.json" || restoreDir != "" {
				t.Fatalf("key=%q restore=%q", key, restoreDir)
			}
			return archive.VerifiedCommit{
				Document: archive.CommitDocument{WindowStart: testWindow, WindowEnd: testWindow.Add(time.Hour)},
				ETag:     "etag-v2", RecordCount: 407,
			}, nil
		},
	}
	out := &bytes.Buffer{}
	if err := runCLI(context.Background(), []string{
		"verify", "--window-start", testWindow.Format(time.RFC3339),
	}, out, deps); err != nil {
		t.Fatalf("runCLI()=%v", err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["verified"] != true || receipt["deletion_authorized"] != false || receipt["record_count"] != float64(407) {
		t.Fatalf("receipt=%v", receipt)
	}
}

func repairTestDeps(db *sql.DB) cliDeps {
	return cliDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone:  "UTC",
				Database:  config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
				QaArchive: config.QaArchiveConfig{Enabled: true, Storage: testStorage()},
			}, nil
		},
		openDB: func(string, string) (*sql.DB, error) { return db, nil },
		newObjectStore: func(context.Context, config.QACaptureStorageConfig) (archive.ObjectStore, error) {
			return archive.NewMemoryObjectStore(), nil
		},
		cleanupHoldActive: func(context.Context, *sql.DB) (bool, error) { return true, nil },
		readSafetyProof:   func() ([]byte, error) { return validSafetyProof(testWindow), nil },
		now:               func() time.Time { return testWindow.Add(2*time.Hour + time.Minute) },
		reconcile: func(context.Context, *sql.Conn, archive.ObjectStore, archive.Window, string, string) (archive.ReconcileReceipt, error) {
			return archive.ReconcileReceipt{}, errors.New("unexpected reconcile")
		},
	}
}

func validSafetyProof(window time.Time) []byte {
	payload, _ := json.Marshal(safetyProof{
		SchemaVersion: "qa-archive-safety-v1", WindowStart: window,
		CheckedAt:           window.Add(2 * time.Hour),
		MaintenanceDisabled: true, MaintenanceInactive: true,
		StaleCleanupDisabled: true, StaleCleanupInactive: true,
		CleanupRuntimeDisabled: true, CleanupLockInactive: true,
	})
	return payload
}

func testStorage() config.QACaptureStorageConfig {
	return config.QACaptureStorageConfig{Driver: "s3", Region: "us-east-1", Bucket: "qa-raw", Prefix: "raw/v1"}
}

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

func TestUS045_RepairApplyIsUnavailableBeforeDependencies(t *testing.T) {
	called := false
	deps := cliDeps{loadConfig: func() (*config.Config, error) {
		called = true
		return nil, errors.New("must not run")
	}}
	err := runCLI(context.Background(), []string{
		"repair-apply", "--window-start", testWindow.Format(time.RFC3339),
	}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), `unknown command "repair-apply"`) {
		t.Fatalf("runCLI() error=%v", err)
	}
	if called {
		t.Fatal("retired repair-apply touched dependencies")
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

func TestUS045_WorkstationRecoveryInspectUsesDirectS3WithoutAppConfigOrDatabase(t *testing.T) {
	calledConfig := false
	calledDB := false
	calledRecovery := false
	deps := cliDeps{
		loadConfig: func() (*config.Config, error) { calledConfig = true; return nil, errors.New("must not run") },
		openDB:     func(string, string) (*sql.DB, error) { calledDB = true; return nil, errors.New("must not run") },
		newRecoveryStore: func(_ context.Context, input archive.WorkstationRecoveryConfig) (archive.ReadOnlyObjectStore, error) {
			calledRecovery = true
			if input.Region != "us-east-1" || input.Bucket != "tokenkey-prod-qa-raw-archive-123456789012" ||
				input.RoleARN != "arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery" || input.Prefix != "raw/v1" {
				t.Fatalf("recovery input=%+v", input)
			}
			return archive.NewMemoryObjectStore(), nil
		},
		verifyCommit: func(_ context.Context, _ archive.ReadOnlyObjectStore, key, restoreDir string) (archive.VerifiedCommit, error) {
			if key != archive.ShardRelativePrefix(testWindow)+"/commit.json" || restoreDir != "" {
				t.Fatalf("key=%q restoreDir=%q", key, restoreDir)
			}
			return archive.VerifiedCommit{Document: archive.CommitDocument{WindowStart: testWindow, WindowEnd: testWindow.Add(time.Hour)}, ETag: "etag-workstation"}, nil
		},
	}
	out := &bytes.Buffer{}
	err := runCLI(context.Background(), []string{
		"inspect", "--workstation", "--window-start", testWindow.Format(time.RFC3339),
		"--region", "us-east-1", "--bucket", "tokenkey-prod-qa-raw-archive-123456789012",
		"--recovery-role-arn", "arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery",
		"--recovery-run-id", "recovery-test-run",
	}, out, deps)
	if err != nil {
		t.Fatalf("runCLI()=%v", err)
	}
	if calledConfig || calledDB || !calledRecovery {
		t.Fatalf("config=%v db=%v recovery=%v", calledConfig, calledDB, calledRecovery)
	}
	var receipt map[string]any
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["source"] != "ops-workstation-s3" || receipt["metadata_only"] != true ||
		receipt["database_accessed"] != false || receipt["iam_boundary"] != "shared_ec2_instance_role_no_process_isolation" {
		t.Fatalf("receipt=%v", receipt)
	}
	if receipt["bucket"] != "tokenkey-prod-qa-raw-archive-123456789012" ||
		receipt["recovery_role_arn"] != "arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery" {
		t.Fatalf("unbound recovery receipt=%v", receipt)
	}
}

func TestUS045_WorkstationRecoveryRequiresAllDirectS3ParametersBeforeDependencies(t *testing.T) {
	called := false
	deps := cliDeps{newRecoveryStore: func(context.Context, archive.WorkstationRecoveryConfig) (archive.ReadOnlyObjectStore, error) {
		called = true
		return nil, errors.New("must not run")
	}}
	for _, args := range [][]string{
		{"verify", "--workstation", "--window-start", testWindow.Format(time.RFC3339), "--bucket", "bucket", "--recovery-role-arn", "arn:aws:iam::123456789012:role/recovery", "--recovery-run-id", "run"},
		{"verify", "--workstation", "--window-start", testWindow.Format(time.RFC3339), "--region", "us-east-1", "--recovery-role-arn", "arn:aws:iam::123456789012:role/recovery", "--recovery-run-id", "run"},
		{"verify", "--workstation", "--window-start", testWindow.Format(time.RFC3339), "--region", "us-east-1", "--bucket", "bucket", "--recovery-run-id", "run"},
		{"verify", "--workstation", "--window-start", testWindow.Format(time.RFC3339), "--region", "us-east-1", "--bucket", "bucket", "--recovery-role-arn", "arn:aws:iam::123456789012:role/recovery"},
	} {
		if err := runCLI(context.Background(), args, &bytes.Buffer{}, deps); err == nil {
			t.Fatalf("runCLI(%v) unexpectedly succeeded", args)
		}
	}
	if called {
		t.Fatal("recovery store opened before parameter validation")
	}
}

func TestUS045_WorkstationRecoveryInspectReportsMissingAndCorruptEvidenceWithoutDependencies(t *testing.T) {
	for _, code := range []string{archive.IntegrityMissingEvidence, archive.IntegrityCorruptArtifact} {
		t.Run(code, func(t *testing.T) {
			calledConfig := false
			calledDB := false
			deps := cliDeps{
				loadConfig: func() (*config.Config, error) { calledConfig = true; return nil, errors.New("must not run") },
				openDB:     func(string, string) (*sql.DB, error) { calledDB = true; return nil, errors.New("must not run") },
				newRecoveryStore: func(context.Context, archive.WorkstationRecoveryConfig) (archive.ReadOnlyObjectStore, error) {
					return archive.NewMemoryObjectStore(), nil
				},
				verifyCommit: func(context.Context, archive.ReadOnlyObjectStore, string, string) (archive.VerifiedCommit, error) {
					return archive.VerifiedCommit{}, &archive.IntegrityError{Code: code, RequestID: "request-safe", Err: errors.New("fixture")}
				},
			}
			out := &bytes.Buffer{}
			err := runCLI(context.Background(), workstationArgs("inspect"), out, deps)
			if err != nil {
				t.Fatalf("runCLI()=%v", err)
			}
			var receipt map[string]any
			if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt["verified"] != false || receipt["blocked"] != true || receipt["verification_error_code"] != code ||
				receipt["source"] != "ops-workstation-s3" || receipt["database_accessed"] != false {
				t.Fatalf("receipt=%v", receipt)
			}
			if calledConfig || calledDB {
				t.Fatalf("config=%v db=%v", calledConfig, calledDB)
			}
		})
	}
}

func TestUS045_WorkstationRecoveryVerifyUsesDirectS3WithoutAppConfigOrDatabase(t *testing.T) {
	calledConfig := false
	calledDB := false
	deps := cliDeps{
		loadConfig: func() (*config.Config, error) { calledConfig = true; return nil, errors.New("must not run") },
		openDB:     func(string, string) (*sql.DB, error) { calledDB = true; return nil, errors.New("must not run") },
		newRecoveryStore: func(context.Context, archive.WorkstationRecoveryConfig) (archive.ReadOnlyObjectStore, error) {
			return archive.NewMemoryObjectStore(), nil
		},
		verifyCommit: func(context.Context, archive.ReadOnlyObjectStore, string, string) (archive.VerifiedCommit, error) {
			return archive.VerifiedCommit{Document: archive.CommitDocument{WindowStart: testWindow, WindowEnd: testWindow.Add(time.Hour)}, ETag: "verify-etag"}, nil
		},
	}
	out := &bytes.Buffer{}
	if err := runCLI(context.Background(), workstationArgs("verify"), out, deps); err != nil {
		t.Fatalf("runCLI()=%v", err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["verified"] != true || receipt["metadata_only"] != true || receipt["database_accessed"] != false {
		t.Fatalf("receipt=%v", receipt)
	}
	if calledConfig || calledDB {
		t.Fatalf("config=%v db=%v", calledConfig, calledDB)
	}
}

func TestUS045_WorkstationRecoveryRestoreUsesExplicitLocalRootAndSecureModes(t *testing.T) {
	restoreRoot := filepath.Join(t.TempDir(), "workstation-restore-root")
	output := filepath.Join(restoreRoot, "window-20260807T0100Z")
	calledConfig := false
	calledDB := false
	deps := cliDeps{
		loadConfig: func() (*config.Config, error) { calledConfig = true; return nil, errors.New("must not run") },
		openDB:     func(string, string) (*sql.DB, error) { calledDB = true; return nil, errors.New("must not run") },
		newRecoveryStore: func(context.Context, archive.WorkstationRecoveryConfig) (archive.ReadOnlyObjectStore, error) {
			return archive.NewMemoryObjectStore(), nil
		},
		verifyCommit: func(_ context.Context, _ archive.ReadOnlyObjectStore, _ string, restoreDir string) (archive.VerifiedCommit, error) {
			if restoreDir != output {
				t.Fatalf("restoreDir=%q want=%q", restoreDir, output)
			}
			if err := os.Mkdir(restoreDir, 0o700); err != nil {
				return archive.VerifiedCommit{}, err
			}
			if err := os.WriteFile(filepath.Join(restoreDir, "evidence.bin"), []byte("body"), 0o600); err != nil {
				return archive.VerifiedCommit{}, err
			}
			return archive.VerifiedCommit{Document: archive.CommitDocument{WindowStart: testWindow, WindowEnd: testWindow.Add(time.Hour)}, ETag: "restore-etag"}, nil
		},
	}
	args := append(workstationArgs("restore"),
		"--restore-root", restoreRoot, "--output", output,
		"--confirm", windowConfirmation(restoreConfirmationPrefix, testWindow),
	)
	out := &bytes.Buffer{}
	if err := runCLI(context.Background(), args, out, deps); err != nil {
		t.Fatalf("runCLI()=%v", err)
	}
	for path, mode := range map[string]os.FileMode{restoreRoot: 0o700, output: 0o700, filepath.Join(output, "evidence.bin"): 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("path=%s mode=%v err=%v", path, info.Mode().Perm(), err)
		}
	}
	if calledConfig || calledDB {
		t.Fatalf("config=%v db=%v", calledConfig, calledDB)
	}
}

func TestUS045_WorkstationRecoveryRestoreRejectsMissingPrivacyConfirmationBeforeDependencies(t *testing.T) {
	called := false
	args := append(workstationArgs("restore"), "--restore-root", t.TempDir(), "--output", filepath.Join(t.TempDir(), "restore"))
	err := runCLI(context.Background(), args, &bytes.Buffer{}, cliDeps{
		newRecoveryStore: func(context.Context, archive.WorkstationRecoveryConfig) (archive.ReadOnlyObjectStore, error) {
			called = true
			return nil, errors.New("must not run")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "privacy confirmation") {
		t.Fatalf("runCLI() error=%v", err)
	}
	if called {
		t.Fatal("dependencies ran before privacy confirmation")
	}
}

func workstationArgs(command string) []string {
	return []string{
		command, "--workstation", "--window-start", testWindow.Format(time.RFC3339),
		"--region", "us-east-1", "--bucket", "tokenkey-prod-qa-raw-archive-123456789012",
		"--recovery-role-arn", "arn:aws:iam::123456789012:role/recovery",
		"--recovery-run-id", "recovery-test-run",
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
		verifyCommit: func(context.Context, archive.ReadOnlyObjectStore, string, string) (archive.VerifiedCommit, error) {
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
		verifyCommit: func(context.Context, archive.ReadOnlyObjectStore, string, string) (archive.VerifiedCommit, error) {
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
		verifyCommit: func(_ context.Context, _ archive.ReadOnlyObjectStore, key, restoreDir string) (archive.VerifiedCommit, error) {
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

func testStorage() config.QACaptureStorageConfig {
	return config.QACaptureStorageConfig{Driver: "s3", Region: "us-east-1", Bucket: "qa-raw", Prefix: "raw/v1"}
}

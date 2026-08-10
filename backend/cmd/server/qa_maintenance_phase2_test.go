//go:build unit

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestUS045_DefaultPlanEnsuresNormalControlBeforeSourceInspection(t *testing.T) {
	db, mockDB, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	window := us045Window(3)
	mockDB.ExpectQuery("INSERT INTO qa_archive_shards").
		WithArgs(window.Start, window.End, archive.StatePending, archive.ShardPrefix(window.Start)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(45))
	mockDB.ExpectQuery("SELECT COUNT\\(\\*\\)").
		WithArgs(window.Start, window.End).
		WillReturnRows(sqlmock.NewRows([]string{"count", "blob_ref_count"}).AddRow(42, 7))
	mockDB.ExpectExec("UPDATE qa_archive_shards").
		WithArgs(int64(42), int64(7), int64(45)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	plan, err := defaultQAMaintenancePlanShard(
		context.Background(), conn, window.Start, window.End,
		archive.ShardPrefix(window.Start), false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RecordCount != 42 || plan.BlobRefCount != 7 {
		t.Fatalf("plan=%+v", plan)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUS045_NormalFirstBoundedCompensationSkipsSelectionAfterNormalFailure(t *testing.T) {
	normal := us045Window(3)
	selectionCalls := 0
	result, err := runQAMaintenanceArchiveCycle(context.Background(), normal, us045Window(0).Start, qaMaintenanceCycleDeps{
		reconcile: func(context.Context, archive.Window) (archive.ReconcileReceipt, error) {
			return archive.ReconcileReceipt{}, errors.New("normal restore failed")
		},
		selectOldest: func(context.Context, archive.Window, time.Time) (archive.CatchupSelection, bool, error) {
			selectionCalls++
			return archive.CatchupSelection{}, false, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "normal") {
		t.Fatalf("err=%v", err)
	}
	if selectionCalls != 0 || result.CompensationSelection != nil || result.Compensation != nil {
		t.Fatalf("selectionCalls=%d result=%+v", selectionCalls, result)
	}
}

func TestUS045_NormalFirstBoundedCompensationStopsWhenNoCandidateExists(t *testing.T) {
	normal := us045Window(3)
	normalReceipt := us045Receipt(normal, "normal-etag")
	reconcileCalls := 0
	result, err := runQAMaintenanceArchiveCycle(context.Background(), normal, us045Window(0).Start, qaMaintenanceCycleDeps{
		reconcile: func(_ context.Context, got archive.Window) (archive.ReconcileReceipt, error) {
			reconcileCalls++
			if got != normal {
				t.Fatalf("reconcile window=%+v", got)
			}
			return normalReceipt, nil
		},
		selectOldest: func(_ context.Context, got archive.Window, cutoff time.Time) (archive.CatchupSelection, bool, error) {
			if got != normal || !cutoff.Equal(us045Window(0).Start) {
				t.Fatalf("select normal=%+v cutoff=%s", got, cutoff)
			}
			return archive.CatchupSelection{}, false, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reconcileCalls != 1 || result.Normal.CommitETag != "normal-etag" || result.CompensationSelection != nil || result.Compensation != nil {
		t.Fatalf("calls=%d result=%+v", reconcileCalls, result)
	}
}

func TestUS045_NormalFirstBoundedCompensationRunsExactlyOneOldestCandidate(t *testing.T) {
	normal := us045Window(3)
	catchup := us045Window(1)
	called := make([]time.Time, 0, 2)
	result, err := runQAMaintenanceArchiveCycle(context.Background(), normal, us045Window(0).Start, qaMaintenanceCycleDeps{
		reconcile: func(_ context.Context, got archive.Window) (archive.ReconcileReceipt, error) {
			called = append(called, got.Start)
			if got == normal {
				return us045Receipt(normal, "normal-etag"), nil
			}
			if got == catchup {
				return us045Receipt(catchup, "catchup-etag"), nil
			}
			return archive.ReconcileReceipt{}, errors.New("unexpected window")
		},
		selectOldest: func(context.Context, archive.Window, time.Time) (archive.CatchupSelection, bool, error) {
			return archive.CatchupSelection{Window: catchup, ShardID: 46, Disposition: archive.CatchupDispositionReconcile}, true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(called) != 2 || !called[0].Equal(normal.Start) || !called[1].Equal(catchup.Start) {
		t.Fatalf("called=%v", called)
	}
	if result.CompensationSelection == nil || result.CompensationSelection.ShardID != 46 || result.Compensation == nil || result.Compensation.CommitETag != "catchup-etag" {
		t.Fatalf("result=%+v", result)
	}
}

func TestUS045_NormalFirstBoundedCompensationKeepsNormalSuccessWhenCatchupFails(t *testing.T) {
	normal := us045Window(3)
	catchup := us045Window(1)
	result, err := runQAMaintenanceArchiveCycle(context.Background(), normal, us045Window(0).Start, qaMaintenanceCycleDeps{
		reconcile: func(_ context.Context, got archive.Window) (archive.ReconcileReceipt, error) {
			if got == normal {
				return us045Receipt(normal, "normal-etag"), nil
			}
			return archive.ReconcileReceipt{}, errors.New("catchup checksum mismatch")
		},
		selectOldest: func(context.Context, archive.Window, time.Time) (archive.CatchupSelection, bool, error) {
			return archive.CatchupSelection{Window: catchup, ShardID: 46, Disposition: archive.CatchupDispositionReconcile}, true, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "compensation") {
		t.Fatalf("err=%v", err)
	}
	if result.Normal.CommitETag != "normal-etag" || result.CompensationSelection == nil || result.Compensation != nil {
		t.Fatalf("result=%+v", result)
	}
}

func TestUS045_NormalFirstBoundedCompensationReportsSelectionAndTerminalFailures(t *testing.T) {
	normal := us045Window(3)
	for _, test := range []struct {
		name            string
		selectCandidate func(context.Context, archive.Window, time.Time) (archive.CatchupSelection, bool, error)
		want            string
	}{
		{
			name: "selection failure",
			selectCandidate: func(context.Context, archive.Window, time.Time) (archive.CatchupSelection, bool, error) {
				return archive.CatchupSelection{}, false, errors.New("cutover unavailable")
			},
			want: "select compensation",
		},
		{
			name: "terminal classification",
			selectCandidate: func(context.Context, archive.Window, time.Time) (archive.CatchupSelection, bool, error) {
				return archive.CatchupSelection{Window: us045Window(1), ShardID: 46, Disposition: archive.CatchupDispositionSourceUnavailableAfterRetention}, true, nil
			},
			want: archive.IntegritySourceUnavailableAfterRetention,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reconcileCalls := 0
			result, err := runQAMaintenanceArchiveCycle(context.Background(), normal, us045Window(0).Start, qaMaintenanceCycleDeps{
				reconcile: func(_ context.Context, got archive.Window) (archive.ReconcileReceipt, error) {
					reconcileCalls++
					return us045Receipt(got, "normal-etag"), nil
				},
				selectOldest: test.selectCandidate,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v", err)
			}
			if reconcileCalls != 1 || result.Normal.CommitETag != "normal-etag" {
				t.Fatalf("calls=%d result=%+v", reconcileCalls, result)
			}
		})
	}
}

func TestUS045_QAMaintenanceCommandReportsCommittedNormalAndCompensationFacts(t *testing.T) {
	t.Setenv("QA_MAINTENANCE_RUN_ID", "run-045")
	t.Setenv("QA_MAINTENANCE_TRIGGER", "operator")
	db, mockDB, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mockDB.ExpectPing()
	mockDB.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mockDB.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mockDB.ExpectClose()

	normal := us045Window(3)
	compensation := us045Window(1)
	now := normal.End.Add(15 * time.Minute)
	normalReceipt := us045Receipt(normal, "normal-etag")
	normalReceipt.SegmentCount = 2
	normalReceipt.RecordCount = 42
	normalReceipt.BlobRefCount = 7
	normalReceipt.BlobPresentCount = 6
	normalReceipt.BlobMissingCount = 1
	compensationReceipt := us045Receipt(compensation, "compensation-etag")
	compensationReceipt.RecordCount = 11
	compensationReceipt.BlobRefCount = 3
	compensationReceipt.BlobPresentCount = 3
	var heartbeat *service.OpsUpsertJobHeartbeatInput
	reconciled := make([]time.Time, 0, 2)
	deps := us045CommandDeps(db, now)
	deps.reconcileShard = func(_ context.Context, _ *sql.Conn, _ archive.ObjectStore, window archive.Window, _, _ string) (archive.ReconcileReceipt, error) {
		reconciled = append(reconciled, window.Start)
		switch window {
		case normal:
			return normalReceipt, nil
		case compensation:
			return compensationReceipt, nil
		default:
			return archive.ReconcileReceipt{}, errors.New("unexpected reconcile window")
		}
	}
	deps.selectOldest = func(_ context.Context, _ *sql.Conn, gotNormal archive.Window, _ time.Time) (archive.CatchupSelection, bool, error) {
		if gotNormal != normal {
			t.Fatalf("normal=%+v", gotNormal)
		}
		return archive.CatchupSelection{Window: compensation, ShardID: 46, Disposition: archive.CatchupDispositionReconcile}, true, nil
	}
	deps.writeHeartbeat = func(_ context.Context, _ *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
		heartbeat = input
		return nil
	}

	out := &bytes.Buffer{}
	if err := runQAMaintenanceCommand(context.Background(), []string{
		"--qa-maintenance-once", "--confirm", qaMaintenanceConfirmation,
	}, out, deps); err != nil {
		t.Fatal(err)
	}
	if len(reconciled) != 2 || !reconciled[0].Equal(normal.Start) || !reconciled[1].Equal(compensation.Start) {
		t.Fatalf("reconciled=%v", reconciled)
	}
	if heartbeat == nil || heartbeat.LastResult == nil {
		t.Fatalf("heartbeat=%+v", heartbeat)
	}
	for _, fact := range []string{
		"status=committed",
		"run_id=run-045",
		"trigger=operator",
		"normal_commit_etag=normal-etag",
		"normal_segment_count=2",
		"normal_aggregate_record_count=42",
		"normal_aggregate_blob_ref_count=7",
		"normal_blob_present_count=6",
		"normal_blob_missing_count=1",
		"normal_restore_verified=true",
		"compensation_commit_etag=compensation-etag",
		"compensation_aggregate_record_count=11",
		"compensation_restore_verified=true",
		"cleanup_eligible=false",
		"deletion_authorized=false",
		"upload_authorized=true",
	} {
		if !strings.Contains(*heartbeat.LastResult, fact) {
			t.Fatalf("heartbeat result %q missing %q", *heartbeat.LastResult, fact)
		}
	}
	var receipt struct {
		RunID              string `json:"run_id"`
		Trigger            string `json:"trigger"`
		DeletionAuthorized bool   `json:"deletion_authorized"`
		UploadAuthorized   bool   `json:"upload_authorized"`
		Plan               struct {
			CommitETag      string `json:"commit_etag"`
			RestoreVerified bool   `json:"restore_verified"`
			CleanupEligible *bool  `json:"cleanup_eligible"`
		} `json:"plan"`
		Compensation *struct {
			CommitETag      string `json:"commit_etag"`
			RestoreVerified bool   `json:"restore_verified"`
			CleanupEligible *bool  `json:"cleanup_eligible"`
		} `json:"compensation"`
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.RunID != "run-045" || receipt.Trigger != "operator" || receipt.DeletionAuthorized || !receipt.UploadAuthorized || receipt.Plan.CommitETag != "normal-etag" ||
		!receipt.Plan.RestoreVerified || receipt.Plan.CleanupEligible == nil || *receipt.Plan.CleanupEligible ||
		receipt.Compensation == nil || receipt.Compensation.CommitETag != "compensation-etag" ||
		!receipt.Compensation.RestoreVerified || receipt.Compensation.CleanupEligible == nil || *receipt.Compensation.CleanupEligible {
		t.Fatalf("receipt=%+v", receipt)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUS045_QAMaintenanceCommandFailureHeartbeatPreservesNormalSuccess(t *testing.T) {
	t.Setenv("QA_MAINTENANCE_RUN_ID", "run-failure-045")
	t.Setenv("QA_MAINTENANCE_TRIGGER", "timer")
	normal := us045Window(3)
	compensation := us045Window(1)
	for _, test := range []struct {
		name            string
		selectOldest    func(context.Context, *sql.Conn, archive.Window, time.Time) (archive.CatchupSelection, bool, error)
		compensationErr error
		wantFacts       []string
	}{
		{
			name: "selection failure",
			selectOldest: func(context.Context, *sql.Conn, archive.Window, time.Time) (archive.CatchupSelection, bool, error) {
				return archive.CatchupSelection{}, false, errors.New("cutover unavailable")
			},
			wantFacts: []string{"stage=compensation_select", "error_code=compensation_selection_failed"},
		},
		{
			name: "compensation reconcile failure",
			selectOldest: func(context.Context, *sql.Conn, archive.Window, time.Time) (archive.CatchupSelection, bool, error) {
				return archive.CatchupSelection{Window: compensation, ShardID: 46, Disposition: archive.CatchupDispositionReconcile}, true, nil
			},
			compensationErr: &archive.IntegrityError{Code: archive.IntegrityCorruptArtifact, Err: errors.New("checksum mismatch")},
			wantFacts: []string{
				"stage=compensation_reconcile",
				"error_code=" + archive.IntegrityCorruptArtifact,
				"compensation_state=" + archive.StateFailed,
				"compensation_error_code=" + archive.IntegrityCorruptArtifact,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mockDB, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			mockDB.ExpectPing()
			mockDB.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
			mockDB.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
			mockDB.ExpectClose()

			var heartbeat *service.OpsUpsertJobHeartbeatInput
			unlocked := false
			deps := us045CommandDeps(db, normal.End.Add(15*time.Minute))
			deps.unlockAdvisory = func(context.Context, *sql.Conn) error {
				unlocked = true
				return nil
			}
			deps.selectOldest = test.selectOldest
			deps.reconcileShard = func(_ context.Context, _ *sql.Conn, _ archive.ObjectStore, window archive.Window, _, _ string) (archive.ReconcileReceipt, error) {
				if window == normal {
					receipt := us045Receipt(normal, "normal-etag")
					receipt.RecordCount = 42
					return receipt, nil
				}
				return archive.ReconcileReceipt{}, test.compensationErr
			}
			deps.writeHeartbeat = func(_ context.Context, gotDB *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
				if !unlocked || gotDB.Stats().InUse != 0 {
					t.Fatalf("failure heartbeat ran before unlock/connection release: unlocked=%t in_use=%d", unlocked, gotDB.Stats().InUse)
				}
				heartbeat = input
				return nil
			}

			err = runQAMaintenanceCommand(context.Background(), []string{
				"--qa-maintenance-once", "--confirm", qaMaintenanceConfirmation,
			}, &bytes.Buffer{}, deps)
			if err == nil {
				t.Fatal("runQAMaintenanceCommand() unexpectedly succeeded")
			}
			if heartbeat == nil || heartbeat.LastErrorAt == nil || heartbeat.LastSuccessAt != nil || heartbeat.LastResult == nil {
				t.Fatalf("heartbeat=%+v", heartbeat)
			}
			for _, fact := range append([]string{
				"status=failed",
				"run_id=run-failure-045",
				"trigger=timer",
				"normal_commit_etag=normal-etag",
				"normal_restore_verified=true",
				"normal_aggregate_record_count=42",
				"deletion_authorized=false",
				"upload_authorized=true",
			}, test.wantFacts...) {
				if !strings.Contains(*heartbeat.LastResult, fact) {
					t.Fatalf("heartbeat result %q missing %q", *heartbeat.LastResult, fact)
				}
			}
			if err := mockDB.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUS045_QAMaintenanceCommandUnlockFailureHeartbeatPreservesNormalSuccess(t *testing.T) {
	db, mockDB, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mockDB.ExpectPing()
	mockDB.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mockDB.ExpectExec("SET statement_timeout = '120s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mockDB.ExpectClose()

	normal := us045Window(3)
	unlockCalls := 0
	var heartbeat *service.OpsUpsertJobHeartbeatInput
	deps := us045CommandDeps(db, normal.End.Add(15*time.Minute))
	deps.reconcileShard = func(_ context.Context, _ *sql.Conn, _ archive.ObjectStore, window archive.Window, _, _ string) (archive.ReconcileReceipt, error) {
		return us045Receipt(window, "normal-etag"), nil
	}
	deps.selectOldest = func(context.Context, *sql.Conn, archive.Window, time.Time) (archive.CatchupSelection, bool, error) {
		return archive.CatchupSelection{}, false, nil
	}
	deps.unlockAdvisory = func(context.Context, *sql.Conn) error {
		unlockCalls++
		return errors.New("unlock unavailable")
	}
	deps.writeHeartbeat = func(_ context.Context, gotDB *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
		if unlockCalls != 2 || gotDB.Stats().InUse != 0 {
			t.Fatalf("failure heartbeat ran before final unlock/connection release: unlock_calls=%d in_use=%d", unlockCalls, gotDB.Stats().InUse)
		}
		heartbeat = input
		return nil
	}

	err = runQAMaintenanceCommand(context.Background(), []string{
		"--qa-maintenance-once", "--confirm", qaMaintenanceConfirmation,
	}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "release qa maintenance advisory lock") {
		t.Fatalf("err=%v", err)
	}
	if heartbeat == nil || heartbeat.LastResult == nil {
		t.Fatalf("heartbeat=%+v", heartbeat)
	}
	for _, fact := range []string{
		"status=failed",
		"stage=advisory_unlock",
		"error_code=advisory_unlock_failed",
		"normal_commit_etag=normal-etag",
		"normal_restore_verified=true",
		"deletion_authorized=false",
		"upload_authorized=true",
	} {
		if !strings.Contains(*heartbeat.LastResult, fact) {
			t.Fatalf("heartbeat result %q missing %q", *heartbeat.LastResult, fact)
		}
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func us045CommandDeps(db *sql.DB, now time.Time) qaMaintenanceDeps {
	return qaMaintenanceDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{
				Timezone: "UTC",
				Database: config.DatabaseConfig{Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable"},
				QaArchive: config.QaArchiveConfig{
					Enabled: true, SealDelayMinutes: 15,
					Storage: config.QACaptureStorageConfig{Driver: "s3", Region: "us-east-1", Bucket: "qa-raw", Prefix: "raw/v1"},
				},
			}, nil
		},
		openDB:          func(string, string) (*sql.DB, error) { return db, nil },
		tryAdvisoryLock: func(context.Context, *sql.Conn) (bool, error) { return true, nil },
		unlockAdvisory:  func(context.Context, *sql.Conn) error { return nil },
		newObjectStore: func(context.Context, config.QACaptureStorageConfig) (archive.ObjectStore, error) {
			return archive.NewMemoryObjectStore(), nil
		},
		now: func() time.Time { return now },
	}
}

func us045Window(hour int) archive.Window {
	start := time.Date(2026, 8, 7, hour, 0, 0, 0, time.UTC)
	return archive.Window{Start: start, End: start.Add(time.Hour)}
}

func us045Receipt(window archive.Window, etag string) archive.ReconcileReceipt {
	return archive.ReconcileReceipt{
		WindowStart: window.Start, WindowEnd: window.End,
		CommitKey:  archive.ShardRelativePrefix(window.Start) + "/commit.json",
		CommitETag: etag, SegmentCount: 1, RecordCount: 42,
	}
}

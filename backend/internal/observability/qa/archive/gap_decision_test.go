//go:build unit

package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestUS045_BuildGapDecisionPlanSelectsOnlyExpiredEmptyNonterminalHours(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	anchor := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	cutoverStart := time.Date(2026, 8, 7, 21, 0, 0, 0, time.UTC)
	cutoverEnd := cutoverStart.Add(time.Hour)
	missing := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	pending := missing.Add(time.Hour)
	terminal := pending.Add(time.Hour)
	committed := terminal.Add(time.Hour)
	sourcePresent := committed.Add(time.Hour)
	pendingUpdatedAt := anchor.Add(-2 * time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery("pg_try_advisory_xact_lock").
		WithArgs(MaintenanceAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("date_trunc\\('hour', clock_timestamp\\(\\)\\)").
		WillReturnRows(sqlmock.NewRows([]string{"anchor", "latest_normal"}).AddRow(anchor, anchor.Add(-time.Hour)))
	mock.ExpectQuery("WHERE forward_cutover=true").
		WillReturnRows(sqlmock.NewRows([]string{"window_start", "window_end"}).AddRow(cutoverStart, cutoverEnd))
	mock.ExpectQuery("generate_series").
		WithArgs(cutoverEnd, anchor.Add(-time.Hour), anchor.Add(-24*time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{
			"window_start", "shard_id", "window_end", "state", "verification_error_code", "updated_at",
			"segment_fingerprint", "has_commit_ready_segment", "source_record_count",
		}).
			AddRow(missing, nil, nil, nil, nil, nil, "", false, int64(0)).
			AddRow(pending, int64(42), pending.Add(time.Hour), StatePending, nil, pendingUpdatedAt, "61:writing:2026-08-12T02:00:00.000000Z", false, int64(0)).
			AddRow(terminal, int64(43), terminal.Add(time.Hour), StateFailed, IntegritySourceUnavailableAfterRetention, anchor, "", false, int64(0)).
			AddRow(committed, int64(44), committed.Add(time.Hour), StateCommitted, nil, anchor, "", true, int64(0)).
			AddRow(sourcePresent, int64(45), sourcePresent.Add(time.Hour), StatePending, nil, anchor, "", false, int64(1)))
	mock.ExpectCommit()

	plan, err := BuildGapDecisionDBPlan(context.Background(), db)
	if err != nil {
		t.Fatalf("BuildGapDecisionDBPlan() err=%v", err)
	}
	if !plan.DBUTCAnchor.Equal(anchor) || !plan.RetentionCutoff.Equal(anchor.Add(-24*time.Hour)) ||
		!plan.ForwardCutover.WindowStart.Equal(cutoverStart) || !plan.LatestNormalWindowStart.Equal(anchor.Add(-time.Hour)) {
		t.Fatalf("plan anchors=%+v", plan)
	}
	if len(plan.Windows) != 2 || !plan.Windows[0].WindowStart.Equal(missing) ||
		plan.Windows[0].Control.Exists || plan.Windows[0].SourceRecordCount != 0 ||
		!plan.Windows[1].WindowStart.Equal(pending) || plan.Windows[1].Control.ShardID != 42 ||
		!plan.Windows[1].Control.WindowEnd.Equal(pending.Add(time.Hour)) ||
		!plan.Windows[1].Control.UpdatedAt.Equal(pendingUpdatedAt) ||
		plan.Windows[1].Control.SegmentFingerprint != "61:writing:2026-08-12T02:00:00.000000Z" {
		t.Fatalf("plan windows=%+v", plan.Windows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUS045_ApplyGapDecisionRejectsSourceOrControlDriftAtomically(t *testing.T) {
	anchor := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	window := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	plan := testGapDecisionPlan(anchor, window)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery("pg_try_advisory_xact_lock").
		WithArgs(MaintenanceAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("FROM qa_archive_gap_decision_receipts").
		WithArgs(plan.PlanHash).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("date_trunc\\('hour', clock_timestamp\\(\\)\\)").
		WillReturnRows(sqlmock.NewRows([]string{"anchor", "latest_normal"}).AddRow(anchor, anchor.Add(-time.Hour)))
	mock.ExpectQuery("WHERE forward_cutover=true").
		WillReturnRows(sqlmock.NewRows([]string{"window_start", "window_end"}).
			AddRow(plan.ForwardCutover.WindowStart, plan.ForwardCutover.WindowEnd))
	mock.ExpectQuery("FROM qa_archive_shards").
		WithArgs(window).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT count\\(\\*\\)").
		WithArgs(window, window.Add(time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectRollback()

	_, err = ApplyGapDecisionPlan(context.Background(), db, plan, "feng")
	if err == nil || !strings.Contains(err.Error(), "source row count drift") {
		t.Fatalf("ApplyGapDecisionPlan() err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUS045_ApplyGapDecisionRejectsSegmentFingerprintDrift(t *testing.T) {
	anchor := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	window := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	plan := testGapDecisionPlan(anchor, window)
	plan.Windows[0].Control = GapDecisionControl{
		Exists: true, ShardID: 42, State: StatePending,
		WindowEnd: window.Add(time.Hour), UpdatedAt: anchor.Add(-time.Hour), SegmentFingerprint: "",
	}
	plan.PlanHash, _ = HashGapDecisionPlan(plan)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery("pg_try_advisory_xact_lock").WithArgs(MaintenanceAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("FROM qa_archive_gap_decision_receipts").WithArgs(plan.PlanHash).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("date_trunc\\('hour', clock_timestamp\\(\\)\\)").
		WillReturnRows(sqlmock.NewRows([]string{"anchor", "latest_normal"}).AddRow(anchor, anchor.Add(-time.Hour)))
	mock.ExpectQuery("WHERE forward_cutover=true").
		WillReturnRows(sqlmock.NewRows([]string{"window_start", "window_end"}).
			AddRow(plan.ForwardCutover.WindowStart, plan.ForwardCutover.WindowEnd))
	mock.ExpectQuery("FROM qa_archive_shards").WithArgs(window).
		WillReturnRows(sqlmock.NewRows([]string{"id", "window_end", "state", "verification_error_code", "updated_at", "segment_fingerprint", "has_commit_ready_segment"}).
			AddRow(int64(42), window.Add(time.Hour), StatePending, nil, anchor.Add(-time.Hour), "63:writing:2026-08-12T04:00:00.000000Z", false))
	mock.ExpectRollback()

	_, err = ApplyGapDecisionPlan(context.Background(), db, plan, "feng")
	if err == nil || !strings.Contains(err.Error(), "control drift") {
		t.Fatalf("ApplyGapDecisionPlan() err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUS045_CompleteGapDecisionPlanExcludesHoursWithRecoveryCommit(t *testing.T) {
	anchor := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	first := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	dbPlan := testGapDecisionPlan(anchor, first)
	dbPlan.Region, dbPlan.Bucket, dbPlan.RecoveryRoleARN, dbPlan.RecoveryRunID, dbPlan.PlanHash = "", "", "", "", ""
	second := dbPlan.Windows[0]
	second.WindowStart = first.Add(time.Hour)
	second.WindowEnd = second.WindowStart.Add(time.Hour)
	second.CommitKey = ShardPrefix(second.WindowStart) + "/commit.json"
	dbPlan.Windows = append(dbPlan.Windows, second)

	plan, err := CompleteGapDecisionPlan(
		dbPlan,
		"us-east-1",
		"tokenkey-prod-qa-raw-archive-123456789012",
		"arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery",
		"recovery-head-batch-20260812T050000Z",
		map[string]bool{
			dbPlan.Windows[0].CommitKey: true,
			dbPlan.Windows[1].CommitKey: false,
		},
	)
	if err != nil {
		t.Fatalf("CompleteGapDecisionPlan() err=%v", err)
	}
	if len(plan.Windows) != 1 || !plan.Windows[0].WindowStart.Equal(second.WindowStart) ||
		!gapDecisionHashPattern.MatchString(plan.PlanHash) {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestUS045_CompleteGapDecisionPlanFromRecoveryStoreBindsEveryHeadResult(t *testing.T) {
	anchor := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	window := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	dbPlan := testGapDecisionPlan(anchor, window)
	dbPlan.Region, dbPlan.Bucket, dbPlan.RecoveryRoleARN, dbPlan.RecoveryRunID, dbPlan.PlanHash = "", "", "", "", ""
	store := NewMemoryObjectStore()

	plan, err := CompleteGapDecisionPlanFromStore(
		context.Background(), dbPlan, store,
		"us-east-1", "tokenkey-prod-qa-raw-archive-123456789012",
		"arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery",
		"recovery-head-batch-20260812T050000Z",
	)
	if err != nil {
		t.Fatalf("CompleteGapDecisionPlanFromStore() err=%v", err)
	}
	if len(plan.Windows) != 1 || plan.Windows[0].CommitExists || plan.PlanHash == "" {
		t.Fatalf("plan=%+v", plan)
	}

	dbPlan.PlanHash = ""
	relativeKey := strings.TrimPrefix(dbPlan.Windows[0].CommitKey, RawV1Prefix+"/")
	if err := store.Put(context.Background(), relativeKey, []byte(`{}`), "application/json"); err != nil {
		t.Fatal(err)
	}
	_, err = CompleteGapDecisionPlanFromStore(
		context.Background(), dbPlan, store,
		"us-east-1", "tokenkey-prod-qa-raw-archive-123456789012",
		"arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery",
		"recovery-head-batch-20260812T050000Z",
	)
	if err == nil || !strings.Contains(err.Error(), "no confirmed missing commit") {
		t.Fatalf("CompleteGapDecisionPlanFromStore() err=%v", err)
	}
}

func TestUS045_CompleteGapDecisionPlanRejectsInvalidTimelineBeforeRecoveryReads(t *testing.T) {
	anchor := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	validWindow := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*GapDecisionPlan)
	}{
		{
			name: "non UTC hour",
			mutate: func(plan *GapDecisionPlan) {
				plan.Windows[0].WindowStart = validWindow.Add(30 * time.Minute)
				plan.Windows[0].WindowEnd = plan.Windows[0].WindowStart.Add(time.Hour)
				plan.Windows[0].CommitKey = ShardPrefix(plan.Windows[0].WindowStart) + "/commit.json"
			},
		},
		{
			name: "before forward cutover end",
			mutate: func(plan *GapDecisionPlan) {
				plan.Windows[0].WindowStart = plan.ForwardCutover.WindowStart.Add(-time.Hour)
				plan.Windows[0].WindowEnd = plan.Windows[0].WindowStart.Add(time.Hour)
				plan.Windows[0].CommitKey = ShardPrefix(plan.Windows[0].WindowStart) + "/commit.json"
			},
		},
		{
			name: "existing control without bound window end",
			mutate: func(plan *GapDecisionPlan) {
				plan.Windows[0].Control = GapDecisionControl{
					Exists: true, ShardID: 42, State: StatePending,
					UpdatedAt: anchor.Add(-time.Hour), SegmentFingerprint: "",
				}
			},
		},
		{
			name: "commit ready segment",
			mutate: func(plan *GapDecisionPlan) {
				plan.Windows[0].Control = GapDecisionControl{
					Exists: true, ShardID: 42, State: StatePending,
					WindowEnd:             plan.Windows[0].WindowEnd,
					UpdatedAt:             anchor.Add(-time.Hour),
					SegmentFingerprint:    "63:verified:2026-08-12T04:00:00.000000Z",
					HasCommitReadySegment: true,
				}
			},
		},
		{
			name: "control state and failure code conflict",
			mutate: func(plan *GapDecisionPlan) {
				plan.Windows[0].Control = GapDecisionControl{
					Exists: true, ShardID: 42, WindowEnd: plan.Windows[0].WindowEnd,
					State: StatePending, VerificationErrorCode: "archive_failed",
					UpdatedAt: anchor.Add(-time.Hour), SegmentFingerprint: "",
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := testGapDecisionPlan(anchor, validWindow)
			plan.Region, plan.Bucket, plan.RecoveryRoleARN, plan.RecoveryRunID, plan.PlanHash = "", "", "", "", ""
			tt.mutate(&plan)
			headCalls := 0
			store := &headTrackingStore{inner: NewMemoryObjectStore(), headCalls: &headCalls}

			_, err := CompleteGapDecisionPlanFromStore(
				context.Background(), plan, store,
				"us-east-1", "tokenkey-prod-qa-raw-archive-123456789012",
				"arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery",
				"recovery-head-batch-20260812T050000Z",
			)
			if err == nil {
				t.Fatal("CompleteGapDecisionPlanFromStore() accepted an invalid database plan")
			}
			if headCalls != 0 {
				t.Fatalf("recovery store was read %d times before database plan validation", headCalls)
			}
		})
	}
}

func TestUS045_GapDecisionCanonicalHashMatchesOperatorVector(t *testing.T) {
	plan := testGapDecisionPlan(
		time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC),
	)
	const want = "c81fc61fe234f8364f59e09d5121f1389a6507a4cc755d4aae7182f4f252ab21"
	if plan.PlanHash != want {
		t.Fatalf("HashGapDecisionPlan()=%s want=%s", plan.PlanHash, want)
	}
}

func TestUS045_ApplyGapDecisionRejectsPlanHashTamperingBeforeDatabase(t *testing.T) {
	anchor := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	plan := testGapDecisionPlan(anchor, time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC))
	plan.RecoveryRunID = "reviewed-evidence"

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = ApplyGapDecisionPlan(context.Background(), db, plan, "feng")
	if err == nil || !strings.Contains(err.Error(), "plan hash drift") {
		t.Fatalf("ApplyGapDecisionPlan() err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUS045_ApplyGapDecisionPersistsTerminalRowsAndApprovalReceiptInOneTransaction(t *testing.T) {
	anchor := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	window := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	appliedAt := time.Date(2026, 8, 12, 5, 42, 0, 0, time.UTC)
	plan := testGapDecisionPlan(anchor, window)
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encodedPlan, &plan); err != nil {
		t.Fatal(err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery("pg_try_advisory_xact_lock").
		WithArgs(MaintenanceAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("FROM qa_archive_gap_decision_receipts").
		WithArgs(plan.PlanHash).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("date_trunc\\('hour', clock_timestamp\\(\\)\\)").
		WillReturnRows(sqlmock.NewRows([]string{"anchor", "latest_normal"}).AddRow(anchor, anchor.Add(-time.Hour)))
	mock.ExpectQuery("WHERE forward_cutover=true").
		WillReturnRows(sqlmock.NewRows([]string{"window_start", "window_end"}).
			AddRow(plan.ForwardCutover.WindowStart, plan.ForwardCutover.WindowEnd))
	mock.ExpectQuery("FROM qa_archive_shards").
		WithArgs(window).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT count\\(\\*\\)").
		WithArgs(window, window.Add(time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectExec("INSERT INTO qa_archive_shards").
		WithArgs(window, window.Add(time.Hour), StatePending, ShardPrefix(window)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, state, verification_error_code, restore_verified_at").
		WithArgs(window).
		WillReturnRows(sqlmock.NewRows([]string{"id", "state", "verification_error_code", "restore_verified_at"}).
			AddRow(int64(51), StatePending, nil, nil))
	mock.ExpectExec("UPDATE qa_archive_shards SET").
		WithArgs(StateFailed, IntegritySourceUnavailableAfterRetention, int64(51)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO qa_archive_gap_decision_receipts").
		WithArgs(plan.PlanHash, GapDecisionPlanSchemaVersion, sqlmock.AnyArg(), "feng", 1).
		WillReturnRows(sqlmock.NewRows([]string{"applied_at"}).AddRow(appliedAt))
	mock.ExpectCommit()

	receipt, err := ApplyGapDecisionPlan(context.Background(), db, plan, "feng")
	if err != nil {
		t.Fatalf("ApplyGapDecisionPlan() err=%v", err)
	}
	if receipt.PlanHash != plan.PlanHash || receipt.WindowCount != 1 || receipt.AlreadyApplied ||
		!receipt.AppliedAt.Equal(appliedAt) {
		t.Fatalf("receipt=%+v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func testGapDecisionPlan(anchor, window time.Time) GapDecisionPlan {
	plan := GapDecisionPlan{
		SchemaVersion:           GapDecisionPlanSchemaVersion,
		DBUTCAnchor:             anchor,
		RetentionCutoff:         anchor.Add(-24 * time.Hour),
		ForwardCutover:          GapDecisionAnchor{WindowStart: time.Date(2026, 8, 7, 21, 0, 0, 0, time.UTC), WindowEnd: time.Date(2026, 8, 7, 22, 0, 0, 0, time.UTC)},
		LatestNormalWindowStart: anchor.Add(-time.Hour),
		Region:                  "us-east-1",
		Bucket:                  "tokenkey-prod-qa-raw-archive-123456789012",
		RecoveryRoleARN:         "arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery",
		RecoveryRunID:           "recovery-head-batch-20260812T050000Z",
		Windows: []GapDecisionWindow{{
			WindowStart: window, WindowEnd: window.Add(time.Hour),
			CommitKey: ShardPrefix(window) + "/commit.json", CommitExists: false,
			SourceRecordCount: 0, Control: GapDecisionControl{},
		}},
	}
	hash, err := HashGapDecisionPlan(plan)
	if err != nil {
		panic(err)
	}
	plan.PlanHash = hash
	return plan
}

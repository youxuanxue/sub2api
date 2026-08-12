//go:build integration

package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestUS045_GapDecisionPlanJSONApplyIsAtomicAndIdempotentInPostgreSQL(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(
		ctx, "postgres:18.1-alpine3.23",
		postgres.WithDatabase("qa_archive_gap_decision"),
		postgres.WithUsername("postgres"), postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("start postgres: %v", err)
	}
	defer func() { _ = container.Terminate(ctx) }()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, migration := range []string{
		"tk_004_create_qa_records.sql",
		"tk_069_create_qa_archive_shards.sql",
		"tk_070_qa_archive_closeout_control.sql",
		"tk_072_qa_archive_forward_cutover.sql",
		"tk_075_qa_archive_gap_decision_receipts.sql",
	} {
		body, readErr := migrations.FS.ReadFile(migration)
		require.NoError(t, readErr)
		_, execErr := db.ExecContext(ctx, string(body))
		require.NoError(t, execErr)
	}

	var anchor time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT date_trunc('hour', clock_timestamp())`).Scan(&anchor))
	anchor = anchor.UTC()
	cutoverStart := anchor.Add(-27 * time.Hour)
	latestNormal := anchor.Add(-time.Hour)
	_, err = db.ExecContext(ctx, `
INSERT INTO qa_archive_shards (
    window_start, window_end, generation, state, restore_verified_at, forward_cutover
) VALUES
    ($1,$2,0,'committed',$2,true),
    ($3,$4,0,'committed',$4,false)`,
		cutoverStart, cutoverStart.Add(time.Hour), latestNormal, latestNormal.Add(time.Hour))
	require.NoError(t, err)

	dbPlan, err := BuildGapDecisionDBPlan(ctx, db)
	require.NoError(t, err)
	require.Len(t, dbPlan.Windows, 2)
	require.Equal(t, anchor.Add(-26*time.Hour), dbPlan.Windows[0].WindowStart)
	require.Equal(t, anchor.Add(-25*time.Hour), dbPlan.Windows[1].WindowStart)

	commitExists := make(map[string]bool, len(dbPlan.Windows))
	for _, window := range dbPlan.Windows {
		commitExists[window.CommitKey] = false
	}
	plan, err := CompleteGapDecisionPlan(
		dbPlan,
		"us-east-1",
		"tokenkey-prod-qa-raw-archive-123456789012",
		"arn:aws:iam::123456789012:role/tokenkey-prod-qa-raw-recovery",
		"recovery-head-batch-integration",
		commitExists,
	)
	require.NoError(t, err)
	encoded, err := json.Marshal(plan)
	require.NoError(t, err)
	var roundTripped GapDecisionPlan
	require.NoError(t, json.Unmarshal(encoded, &roundTripped))
	require.Equal(t, plan.PlanHash, roundTripped.PlanHash)

	_, err = db.ExecContext(ctx, `
CREATE FUNCTION reject_gap_decision_receipt_for_test()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'receipt write rejected for atomicity test';
END;
$$;
CREATE TRIGGER reject_gap_decision_receipt_for_test
BEFORE INSERT ON qa_archive_gap_decision_receipts
FOR EACH ROW EXECUTE FUNCTION reject_gap_decision_receipt_for_test();`)
	require.NoError(t, err)
	_, err = ApplyGapDecisionPlan(ctx, db, roundTripped, "feng")
	require.ErrorContains(t, err, "persist approval receipt")

	var terminalCount, receiptCount int
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT count(*) FROM qa_archive_shards
WHERE window_start >= $1 AND window_start < $2`,
		anchor.Add(-26*time.Hour), anchor.Add(-24*time.Hour)).Scan(&terminalCount))
	require.Zero(t, terminalCount, "receipt failure must roll back every terminal shard")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM qa_archive_gap_decision_receipts`).Scan(&receiptCount))
	require.Zero(t, receiptCount)

	_, err = db.ExecContext(ctx, `
DROP TRIGGER reject_gap_decision_receipt_for_test ON qa_archive_gap_decision_receipts;
DROP FUNCTION reject_gap_decision_receipt_for_test();`)
	require.NoError(t, err)
	receipt, err := ApplyGapDecisionPlan(ctx, db, roundTripped, "feng")
	require.NoError(t, err)
	require.Equal(t, plan.PlanHash, receipt.PlanHash)
	require.Equal(t, 2, receipt.WindowCount)
	require.False(t, receipt.AlreadyApplied)
	require.False(t, receipt.AppliedAt.IsZero())

	rows, err := db.QueryContext(ctx, `
SELECT state, verification_error_code, cleanup_eligible
FROM qa_archive_shards
WHERE window_start >= $1 AND window_start < $2
ORDER BY window_start`, anchor.Add(-26*time.Hour), anchor.Add(-24*time.Hour))
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	seen := 0
	for rows.Next() {
		var state, code string
		var cleanupEligible bool
		require.NoError(t, rows.Scan(&state, &code, &cleanupEligible))
		require.Equal(t, StateFailed, state)
		require.Equal(t, IntegritySourceUnavailableAfterRetention, code)
		require.False(t, cleanupEligible)
		seen++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 2, seen)

	var storedPlanHash, approvedBy string
	var storedWindowCount int
	var storedPlan []byte
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT plan_hash, approved_by, window_count, plan_json
FROM qa_archive_gap_decision_receipts`).Scan(
		&storedPlanHash, &approvedBy, &storedWindowCount, &storedPlan,
	))
	require.Equal(t, plan.PlanHash, storedPlanHash)
	require.Equal(t, "feng", approvedBy)
	require.Equal(t, 2, storedWindowCount)
	var persisted GapDecisionPlan
	require.NoError(t, json.Unmarshal(storedPlan, &persisted))
	require.Equal(t, plan.PlanHash, persisted.PlanHash)

	replayed, err := ApplyGapDecisionPlan(ctx, db, roundTripped, "different-operator")
	require.NoError(t, err)
	require.True(t, replayed.AlreadyApplied)
	require.Equal(t, "feng", replayed.ApprovedBy)
	require.Equal(t, receipt.AppliedAt, replayed.AppliedAt)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM qa_archive_gap_decision_receipts`).Scan(&receiptCount))
	require.Equal(t, 1, receiptCount)
}

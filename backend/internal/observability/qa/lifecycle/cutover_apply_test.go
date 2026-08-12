//go:build unit

package lifecycle

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestApplyCutoverActivationKeepsDefaultAndPersistsT0Atomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	anchor := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	t0 := anchor.Add(time.Hour)
	inv := CutoverInventory{
		DBAnchorUTC:          anchor,
		RetentionBoundaryUTC: anchor.Add(-24 * time.Hour),
		HourlyHorizonHours:   HourlyHorizon,
		RequiredFutureHours:  HourlyHorizon,
		DefaultPresent:       true,
		DefaultRowCount:      42,
		Partitions: []InventoryRow{{
			Schema: "public", Name: "qa_records_default", IsDefault: true, Layout: "default", RowCount: 42,
		}},
	}
	plan, err := BuildCutoverPlan(inv, t0)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`LOCK TABLE "qa_records" IN ACCESS EXCLUSIVE MODE`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts`).WithArgs(CutoverPhaseActivate).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow("public", "qa_records_default", "DEFAULT", false, false, true, nil, nil))
	for i := 0; i < HourlyHorizon; i++ {
		mock.ExpectExec(`CREATE TABLE IF NOT EXISTS`).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectQuery(`WITH child_bounds AS`).WithArgs(TableQARecords, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(HourlyHorizon))
	mock.ExpectExec(`INSERT INTO settings`).WithArgs(HourlyStorageCutoverSettingKey, t0.Format(time.RFC3339)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT value FROM settings`).WithArgs(HourlyStorageCutoverSettingKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(t0.Format(time.RFC3339)))
	mock.ExpectExec(`INSERT INTO qa_lifecycle_receipts`).WithArgs(CutoverPhaseActivate, plan.PlanHash, t0).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := ApplyCutoverPlan(context.Background(), db, plan); err != nil {
		t.Fatalf("ApplyCutoverPlan() err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyCutoverFinalizeRequiresMatchingActivationReceipt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	inv := CutoverInventory{
		DBAnchorUTC:             t0.Add(25 * time.Hour),
		HourlyHorizonHours:      HourlyHorizon,
		CoveredFutureHours:      HourlyHorizon,
		RequiredFutureHours:     HourlyHorizon,
		DefaultPresent:          true,
		ArchiveHeartbeatHealthy: true,
	}
	plan, err := BuildCutoverFinalizePlan(inv, t0)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`LOCK TABLE "qa_records" IN ACCESS EXCLUSIVE MODE`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts`).
		WithArgs(CutoverPhaseFinalize).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts`).
		WithArgs(CutoverPhaseActivate).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	err = ApplyCutoverPlan(context.Background(), db, plan)
	if err == nil || !regexp.MustCompile(`activation receipt`).MatchString(err.Error()) {
		t.Fatalf("ApplyCutoverPlan() err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyCutoverFinalizeDropsEmptyDefaultAndPersistsReceipt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	anchor := t0.Add(25 * time.Hour)
	monthlyLower := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	monthlyUpper := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	inv := CutoverInventory{
		DBAnchorUTC:             anchor,
		HourlyHorizonHours:      HourlyHorizon,
		CoveredFutureHours:      HourlyHorizon,
		RequiredFutureHours:     HourlyHorizon,
		DefaultPresent:          true,
		ArchiveHeartbeatHealthy: true,
		Partitions: []InventoryRow{{
			Schema: "public", Name: "qa_records_202604", Layout: "monthly",
			Lower: monthlyLower, Upper: monthlyUpper,
		}},
	}
	plan, err := BuildCutoverFinalizePlan(inv, t0)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`LOCK TABLE "qa_records" IN ACCESS EXCLUSIVE MODE`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts`).
		WithArgs(CutoverPhaseFinalize).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts`).
		WithArgs(CutoverPhaseActivate).
		WillReturnRows(sqlmock.NewRows([]string{"plan_hash", "t0_utc"}).AddRow(strings.Repeat("a", 64), t0))
	mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
	mock.ExpectExec(`INSERT INTO settings`).WithArgs(HourlyStorageCutoverSettingKey, t0.Format(time.RFC3339)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT value FROM settings`).WithArgs(HourlyStorageCutoverSettingKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(t0.Format(time.RFC3339)))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow(
		"public", "qa_records_202604", "FOR VALUES FROM ('2026-04-01') TO ('2026-05-01')",
		false, false, false, monthlyLower, monthlyUpper,
	).AddRow("public", "qa_records_default", "DEFAULT", false, false, true, nil, nil))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM "public"\."qa_records_202604"`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`WITH child_bounds AS`).WithArgs(TableQARecords, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(HourlyHorizon))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM "public"\."qa_records_default"`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(`DROP TABLE IF EXISTS "public"\."qa_records_202604"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`DROP TABLE IF EXISTS "public"\."qa_records_default"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO qa_lifecycle_receipts`).
		WithArgs(CutoverPhaseFinalize, plan.PlanHash, t0).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := ApplyCutoverPlan(context.Background(), db, plan); err != nil {
		t.Fatalf("ApplyCutoverPlan() err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyCutoverFinalizeRejectsMonthlyInventoryDrift(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	anchor := t0.Add(25 * time.Hour)
	monthlyLower := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	monthlyUpper := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	inv := CutoverInventory{
		DBAnchorUTC: anchor, HourlyHorizonHours: HourlyHorizon,
		CoveredFutureHours: HourlyHorizon, RequiredFutureHours: HourlyHorizon,
		DefaultPresent: true, ArchiveHeartbeatHealthy: true,
		Partitions: []InventoryRow{{
			Schema: "public", Name: "qa_records_202604", Layout: "monthly",
			Lower: monthlyLower, Upper: monthlyUpper,
		}},
	}
	plan, err := BuildCutoverFinalizePlan(inv, t0)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`LOCK TABLE "qa_records" IN ACCESS EXCLUSIVE MODE`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts`).
		WithArgs(CutoverPhaseFinalize).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts`).
		WithArgs(CutoverPhaseActivate).
		WillReturnRows(sqlmock.NewRows([]string{"plan_hash", "t0_utc"}).AddRow(strings.Repeat("a", 64), t0))
	mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
	mock.ExpectExec(`INSERT INTO settings`).WithArgs(HourlyStorageCutoverSettingKey, t0.Format(time.RFC3339)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT value FROM settings`).WithArgs(HourlyStorageCutoverSettingKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(t0.Format(time.RFC3339)))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow(
		"public", "qa_records_202604", "FOR VALUES FROM ('2026-04-01') TO ('2026-05-02')",
		false, false, false, monthlyLower, monthlyUpper.Add(24*time.Hour),
	).AddRow("public", "qa_records_default", "DEFAULT", false, false, true, nil, nil))
	mock.ExpectRollback()

	err = ApplyCutoverPlan(context.Background(), db, plan)
	if err == nil || !strings.Contains(err.Error(), "monthly partition inventory drift") {
		t.Fatalf("ApplyCutoverPlan() err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyCutoverFinalizeRejectsMonthlyRowsAddedAfterPlan(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	anchor := t0.Add(25 * time.Hour)
	monthlyLower := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	monthlyUpper := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	inv := CutoverInventory{
		DBAnchorUTC: anchor, HourlyHorizonHours: HourlyHorizon,
		CoveredFutureHours: HourlyHorizon, RequiredFutureHours: HourlyHorizon,
		DefaultPresent: true, ArchiveHeartbeatHealthy: true,
		Partitions: []InventoryRow{{
			Schema: "public", Name: "qa_records_202604", Layout: "monthly",
			Lower: monthlyLower, Upper: monthlyUpper,
		}},
	}
	plan, err := BuildCutoverFinalizePlan(inv, t0)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`LOCK TABLE "qa_records" IN ACCESS EXCLUSIVE MODE`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts`).
		WithArgs(CutoverPhaseFinalize).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts`).
		WithArgs(CutoverPhaseActivate).
		WillReturnRows(sqlmock.NewRows([]string{"plan_hash", "t0_utc"}).AddRow(strings.Repeat("a", 64), t0))
	mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
	mock.ExpectExec(`INSERT INTO settings`).WithArgs(HourlyStorageCutoverSettingKey, t0.Format(time.RFC3339)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT value FROM settings`).WithArgs(HourlyStorageCutoverSettingKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(t0.Format(time.RFC3339)))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow(
		"public", "qa_records_202604", "FOR VALUES FROM ('2026-04-01') TO ('2026-05-01')",
		false, false, false, monthlyLower, monthlyUpper,
	).AddRow("public", "qa_records_default", "DEFAULT", false, false, true, nil, nil))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM "public"\."qa_records_202604"`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	err = ApplyCutoverPlan(context.Background(), db, plan)
	if err == nil || !strings.Contains(err.Error(), "now holds 1 rows") {
		t.Fatalf("ApplyCutoverPlan() err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMatchAppliedCutoverAcceptsOnlySameHashAndT0(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts`).
		WithArgs(CutoverPhaseActivate).
		WillReturnRows(sqlmock.NewRows([]string{"plan_hash", "t0_utc"}).AddRow("a"+strings.Repeat("0", 63), t0))

	applied, err := MatchAppliedCutover(
		context.Background(), db, CutoverPhaseActivate, t0, "a"+strings.Repeat("0", 63),
	)
	if err != nil || !applied {
		t.Fatalf("MatchAppliedCutover() applied=%t err=%v", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

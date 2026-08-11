//go:build unit

package pgpartition

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestOrderRehomeMonthsPrefersCurrentThenFutureThenPast(t *testing.T) {
	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	months := []time.Time{
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}
	got := orderRehomeMonths(months, now)
	want := []time.Time{
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("index %d=%s want %s (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestDefaultRehomeDedupColumnsUsesCompositeIdentityForQARecords(t *testing.T) {
	got := defaultRehomeDedupColumns(qaRecordsRehomeTable)
	want := []string{"created_at", "request_id"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
	clause := rehomeRowNotInStaging("qa_records_2026_08_rehome_staging", "d", got)
	if !strings.Contains(clause, `"created_at"`) || !strings.Contains(clause, `"request_id"`) {
		t.Fatalf("dedup clause missing composite identity: %q", clause)
	}
	if strings.Contains(clause, "request_id = d.request_id") {
		t.Fatalf("dedup must compare created_at and request_id together: %q", clause)
	}
}

func TestRehomeDefaultMonthlyRejectsMissingDedupIdentity(t *testing.T) {
	_, err := RehomeDefaultMonthly(
		context.Background(),
		nil,
		"pgpart_itest_rehome",
		"created_at",
		time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		RehomeOptions{},
	)
	if err == nil || !strings.Contains(err.Error(), "dedup identity columns") {
		t.Fatalf("expected dedup identity error, got %v", err)
	}
}

func TestRehomeDefaultMonthlyBudgetExhaustedDefersFinalize(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dedup := []string{"created_at", "request_id"}

	mock.ExpectQuery("pg_inherits").
		WithArgs("qa_records").
		WillReturnRows(sqlmock.NewRows([]string{"relname", "bound_expr"}).
			AddRow("qa_records_default", "DEFAULT"))
	mock.ExpectQuery("SELECT DISTINCT date_trunc").
		WillReturnRows(sqlmock.NewRows([]string{"month"}).AddRow(start))
	mock.ExpectQuery("SELECT relname").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_202608").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS \"qa_records_2026_08_rehome_staging\"").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\"").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(10)))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_2026_08_rehome_staging\"").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT NOT EXISTS").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"ready"}).AddRow(false))
	mock.ExpectExec("INSERT INTO \"qa_records_2026_08_rehome_staging\"").
		WithArgs(start, end, 3).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(10)))
	mock.ExpectQuery("SELECT relname").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).
			AddRow("qa_records_2026_08_rehome_staging"))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_2026_08_rehome_staging\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(3)))

	result, err := RehomeDefaultMonthly(
		context.Background(), db, "qa_records", "created_at", now,
		RehomeOptions{BatchSize: 5000, MaxRowsPerRun: 3, DedupColumns: dedup},
	)
	if err != nil {
		t.Fatalf("RehomeDefaultMonthly: %v", err)
	}
	if !result.BudgetExhausted || !result.PendingFinalize || result.RowsMoved != 3 || result.RemainingRows != 10 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestRehomeDefaultMonthlyFinalizeUsesParentTableLock(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dedup := []string{"created_at", "request_id"}

	mock.ExpectQuery("pg_inherits").
		WithArgs("qa_records").
		WillReturnRows(sqlmock.NewRows([]string{"relname", "bound_expr"}).
			AddRow("qa_records_default", "DEFAULT"))
	mock.ExpectQuery("SELECT DISTINCT date_trunc").
		WillReturnRows(sqlmock.NewRows([]string{"month"}).AddRow(start))
	mock.ExpectQuery("SELECT relname").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_202608").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS \"qa_records_2026_08_rehome_staging\"").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\"").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_2026_08_rehome_staging\"").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectQuery("SELECT NOT EXISTS").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"ready"}).AddRow(true))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_2026_08_rehome_staging\"").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`LOCK TABLE "qa_records" IN SHARE ROW EXCLUSIVE MODE`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO \"qa_records_2026_08_rehome_staging\"").
		WithArgs(start, end).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT NOT EXISTS").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"ready"}).AddRow(true))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_2026_08_rehome_staging\"").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectExec("DELETE FROM \"qa_records_default\"").
		WithArgs(start, end).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("CREATE TABLE \"qa_records_2026_08\" PARTITION OF \"qa_records\"").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO \"qa_records_2026_08\"").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(`DROP TABLE IF EXISTS "qa_records_2026_08_rehome_staging"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT relname").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}))

	result, err := RehomeDefaultMonthly(
		context.Background(), db, "qa_records", "created_at", now,
		RehomeOptions{BatchSize: 5000, DedupColumns: dedup},
	)
	if err != nil {
		t.Fatalf("RehomeDefaultMonthly: %v", err)
	}
	if result.RowsMoved != 2 || result.RemainingRows != 0 || result.PendingFinalize {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

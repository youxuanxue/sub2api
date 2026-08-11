//go:build unit

package pgpartition

import (
	"context"
	"regexp"
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

func TestResolveMonthlyPartitionNamePrefersAttachedLegacyName(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	month := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	name, err := resolveMonthlyPartitionName(context.Background(), db, "qa_records", month)
	if err != nil {
		t.Fatalf("resolveMonthlyPartitionName: %v", err)
	}
	if name != "qa_records_2026_08" {
		t.Fatalf("name=%q", name)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestRehomeDefaultMonthlyReturnsEarlyWithoutDefaultPartition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery("pg_inherits").
		WithArgs("qa_records").
		WillReturnRows(sqlmock.NewRows([]string{"relname", "bound_expr"}).
			AddRow("qa_records_202608", "FOR VALUES FROM ('2026-08-01') TO ('2026-09-01')"))

	result, err := RehomeDefaultMonthly(context.Background(), db, "qa_records", "created_at", time.Now(), RehomeOptions{BatchSize: 100})
	if err != nil {
		t.Fatalf("RehomeDefaultMonthly: %v", err)
	}
	if result.DefaultPartition != "" || result.RemainingRows != 0 || len(result.Months) != 0 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestRehomeDefaultMonthlyDrainsDefaultBeforeCreatingPartition(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("pg_inherits").
		WithArgs("qa_records").
		WillReturnRows(sqlmock.NewRows([]string{"relname", "bound_expr"}).
			AddRow("qa_records_default", "DEFAULT"))
	mock.ExpectQuery("SELECT DISTINCT date_trunc").
		WillReturnRows(sqlmock.NewRows([]string{"month"}).
			AddRow(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_202608").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").WithArgs("qa_records_2026_08_rehome_staging").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\" WHERE \"created_at\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS \"qa_records_2026_08_rehome_staging\" \\(LIKE \"qa_records_default\"").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("WITH moved AS").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\" WHERE \"created_at\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_2026_08_rehome_staging\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS \"qa_records_2026_08\" PARTITION OF \"qa_records\"").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO \"qa_records_2026_08\" SELECT \\* FROM \"qa_records_2026_08_rehome_staging\"").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(`DROP TABLE IF EXISTS "qa_records_2026_08_rehome_staging"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))

	result, err := RehomeDefaultMonthly(context.Background(), db, "qa_records", "created_at", now, RehomeOptions{BatchSize: 5000})
	if err != nil {
		t.Fatalf("RehomeDefaultMonthly: %v", err)
	}
	if len(result.Months) != 1 || result.Months[0].RowsMoved != 2 || result.RemainingRows != 0 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestRehomeDefaultMonthlyStopsWhenRowBudgetExhausted(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("pg_inherits").
		WithArgs("qa_records").
		WillReturnRows(sqlmock.NewRows([]string{"relname", "bound_expr"}).
			AddRow("qa_records_default", "DEFAULT"))
	mock.ExpectQuery("SELECT DISTINCT date_trunc").
		WillReturnRows(sqlmock.NewRows([]string{"month"}).
			AddRow(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_202608").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("qa_records", "qa_records_2026_08").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").WithArgs("qa_records_2026_08_rehome_staging").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\" WHERE \"created_at\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(10)))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS \"qa_records_2026_08_rehome_staging\" \\(LIKE \"qa_records_default\"").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("WITH moved AS").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(7)))

	result, err := RehomeDefaultMonthly(
		context.Background(), db, "qa_records", "created_at", now,
		RehomeOptions{BatchSize: 5000, MaxRowsPerRun: 3},
	)
	if err != nil {
		t.Fatalf("RehomeDefaultMonthly: %v", err)
	}
	if !result.BudgetExhausted || result.RowsMoved != 3 || result.RemainingRows != 7 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

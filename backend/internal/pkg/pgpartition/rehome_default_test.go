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

	result, err := RehomeDefaultMonthly(context.Background(), db, "qa_records", "created_at", time.Now(), 100)
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

func TestRehomeDefaultMonthlyMovesOneMonthAndAttachesPartition(t *testing.T) {
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
		WithArgs("qa_records", "qa_records_202608").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("CREATE TABLE \"qa_records_202608\"").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("WITH moved AS").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("WITH moved AS").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("ALTER TABLE \"qa_records\" ATTACH PARTITION \"qa_records_202608\"").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))

	result, err := RehomeDefaultMonthly(context.Background(), db, "qa_records", "created_at", now, 5000)
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

func TestRehomeDefaultMonthlyDropsUnusedStagingPartition(t *testing.T) {
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
		WithArgs("qa_records", "qa_records_202608").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(regexp.QuoteMeta(`CREATE TABLE "qa_records_202608"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("WITH moved AS").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DROP TABLE IF EXISTS "qa_records_202608"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM \"qa_records_default\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(5)))

	result, err := RehomeDefaultMonthly(context.Background(), db, "qa_records", "created_at", now, 5000)
	if err != nil {
		t.Fatalf("RehomeDefaultMonthly: %v", err)
	}
	if len(result.Months) != 0 || result.RemainingRows != 5 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

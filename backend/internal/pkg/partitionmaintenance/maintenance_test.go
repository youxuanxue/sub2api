//go:build unit

package partitionmaintenance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

var maintenanceNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

func expectPartitioned(mock sqlmock.Sqlmock, table string, partitioned bool) {
	mock.ExpectQuery("pg_partitioned_table").WithArgs(table).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(partitioned))
}

func expectCreates(mock sqlmock.Sqlmock, count int) {
	for i := 0; i < count; i++ {
		mock.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
	}
}

func expectCoverage(mock sqlmock.Sqlmock, table string, covered int) {
	mock.ExpectQuery("(?s)pg_get_expr.*pg_inherits").
		WithArgs(table, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"covered_ranges"}).AddRow(covered))
}

func expectNoDefaultRehome(mock sqlmock.Sqlmock, table string) {
	mock.ExpectQuery("pg_inherits").
		WithArgs(table).
		WillReturnRows(sqlmock.NewRows([]string{"relname", "bound_expr"}))
}

func TestEnsureStrictCreatesAndVerifiesAllTargets(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, target := range []struct {
		table string
		count int
	}{
		{"ops_system_logs", 4},
		{"ops_error_logs", 4},
		{"qa_records", 4},
		{"usage_logs", 8},
	} {
		expectPartitioned(mock, target.table, true)
		expectCreates(mock, target.count)
		expectCoverage(mock, target.table, target.count)
		if target.table == qaRecordsTable {
			expectNoDefaultRehome(mock, target.table)
		}
	}

	result, err := Ensure(context.Background(), db, maintenanceNow, ModeRequireAllPartitioned)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	want := []TableResult{
		{Table: "ops_system_logs", RangeCount: 4},
		{Table: "ops_error_logs", RangeCount: 4},
		{Table: "qa_records", RangeCount: 4},
		{Table: "usage_logs", RangeCount: 8},
	}
	if len(result.Tables) != len(want) {
		t.Fatalf("tables=%+v want %+v", result.Tables, want)
	}
	for i := range want {
		if result.Tables[i] != want[i] {
			t.Fatalf("tables[%d]=%+v want %+v", i, result.Tables[i], want[i])
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestEnsureStrictRejectsUnpartitionedTarget(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	expectPartitioned(mock, "ops_system_logs", false)

	_, err = Ensure(context.Background(), db, maintenanceNow, ModeRequireAllPartitioned)
	if err == nil || !strings.Contains(err.Error(), "ops_system_logs is not partitioned") {
		t.Fatalf("expected strict unpartitioned error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestEnsureAllowUnpartitionedSkipsCompatibilityTarget(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	expectPartitioned(mock, "ops_system_logs", false)
	expectPartitioned(mock, "ops_error_logs", true)
	expectCreates(mock, 4)
	expectCoverage(mock, "ops_error_logs", 4)
	expectPartitioned(mock, "qa_records", false)
	expectPartitioned(mock, "usage_logs", false)

	result, err := Ensure(context.Background(), db, maintenanceNow, ModeAllowUnpartitioned)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(result.Tables) != 1 || result.Tables[0] != (TableResult{Table: "ops_error_logs", RangeCount: 4}) {
		t.Fatalf("unexpected result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestEnsureRejectsUncoveredOverlap(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	expectPartitioned(mock, "ops_system_logs", true)
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnError(
		&pq.Error{Code: pq.ErrorCode("42P17")},
	)
	expectCreates(mock, 3)
	expectCoverage(mock, "ops_system_logs", 3)

	_, err = Ensure(context.Background(), db, maintenanceNow, ModeRequireAllPartitioned)
	if err == nil || !strings.Contains(err.Error(), "covers 3 of 4 required ranges") {
		t.Fatalf("expected uncovered range error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

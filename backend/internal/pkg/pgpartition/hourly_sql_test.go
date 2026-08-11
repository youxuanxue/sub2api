//go:build unit

package pgpartition

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCountTableRowsQualifiesValidatedRelation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM "qa_schema"\."qa_records_default"`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	count, err := CountTableRows(context.Background(), db, "qa_schema", "qa_records_default")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count=%d want 0", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

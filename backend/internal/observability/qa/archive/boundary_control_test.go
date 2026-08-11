//go:build unit

package archive

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPersistBoundaryTerminalGapMarksCommittedShardWithUncoveredSourceFailed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	window := Window{
		Start: time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC),
	}
	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	mock.ExpectExec("INSERT INTO qa_archive_shards").
		WithArgs(window.Start, window.End, StatePending, ShardPrefix(window.Start)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT id, state, verification_error_code, restore_verified_at").
		WithArgs(window.Start).
		WillReturnRows(sqlmock.NewRows([]string{"id", "state", "verification_error_code", "restore_verified_at"}).
			AddRow(int64(42), StateCommitted, nil, window.End))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(window.Start, window.End, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE qa_archive_shards SET").
		WithArgs(StateFailed, IntegritySourceUnavailableAfterRetention, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	shardID, err := NewSQLControlStore().PersistBoundaryTerminalGap(ctx, tx, window)
	if err != nil {
		t.Fatal(err)
	}
	if shardID != 42 {
		t.Fatalf("shardID=%d want 42", shardID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

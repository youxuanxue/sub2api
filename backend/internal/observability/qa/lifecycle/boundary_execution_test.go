//go:build unit

package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

type lockOrderingControl struct{}

func (lockOrderingControl) InspectCatchupHourTx(
	ctx context.Context,
	tx *sql.Tx,
	_ archive.Window,
) (archive.CatchupHourStatus, error) {
	var sentinel int
	if err := tx.QueryRowContext(ctx, "SELECT 1").Scan(&sentinel); err != nil {
		return archive.CatchupHourStatus{}, err
	}
	return archive.CatchupHourStatus{
		Exists: true, ShardID: 7, State: archive.StateCommitted, RestoreVerified: true,
	}, nil
}

func (lockOrderingControl) PersistBoundaryTerminalGap(
	context.Context,
	*sql.Tx,
	archive.Window,
) (int64, error) {
	return 0, errors.New("unexpected terminal gap")
}

func (lockOrderingControl) RecordSourceDropped(
	context.Context,
	*sql.Tx,
	int64,
	string,
	time.Time,
) error {
	return nil
}

func (lockOrderingControl) RecordHotFilesCleaned(
	context.Context,
	*sql.Conn,
	int64,
	time.Time,
	string,
) error {
	return nil
}

type preserveUnknownControl struct {
	persistCalled bool
	recordedID    int64
	recordedName  string
}

func (c *preserveUnknownControl) InspectCatchupHourTx(
	context.Context,
	*sql.Tx,
	archive.Window,
) (archive.CatchupHourStatus, error) {
	return archive.CatchupHourStatus{
		Exists: true, ShardID: 77, State: archive.StateFailed,
		VerificationErrorCode: archive.IntegrityCommitExistenceUnknown,
	}, nil
}

func (c *preserveUnknownControl) PersistBoundaryTerminalGap(
	context.Context,
	*sql.Tx,
	archive.Window,
) (int64, error) {
	c.persistCalled = true
	return 0, errors.New("unknown commit existence must not be terminalized")
}

func (c *preserveUnknownControl) RecordSourceDropped(
	_ context.Context,
	_ *sql.Tx,
	shardID int64,
	partitionName string,
	_ time.Time,
) error {
	c.recordedID = shardID
	c.recordedName = partitionName
	return nil
}

func (*preserveUnknownControl) RecordHotFilesCleaned(
	context.Context,
	*sql.Conn,
	int64,
	time.Time,
	string,
) error {
	return nil
}

func TestRunBoundaryStopsBeforeExpiryWhenProvisioningFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	anchor := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS`).
		WillReturnError(errors.New("provision failed"))

	_, err = RunBoundary(context.Background(), db, nil, Options{HoursAhead: 1})
	if err == nil || !strings.Contains(err.Error(), "provision failed") {
		t.Fatalf("RunBoundary() err=%v", err)
	}
	if strings.Contains(err.Error(), "was not expected") {
		t.Fatalf("RunBoundary() continued after provisioning failure: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunCutoverProvisionOnlyFillsCurrentHorizonAfterT0(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	anchor := time.Date(2026, 8, 12, 2, 0, 0, 0, time.UTC)
	t0 := anchor.Add(-25 * time.Hour)
	mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
	mock.ExpectQuery(`FROM qa_lifecycle_receipts`).
		WillReturnRows(sqlmock.NewRows([]string{"activate_t0", "finalized"}).AddRow(t0, false))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS "qa_records_20260812_02" PARTITION OF "qa_records"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`WITH child_bounds AS`).
		WithArgs(TableQARecords, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	result, err := RunCutoverProvisionOnly(context.Background(), db, Options{HoursAhead: 1})
	if err != nil {
		t.Fatalf("RunCutoverProvisionOnly() err=%v", err)
	}
	if result.RangesCovered != 1 || result.RangesRequired != 1 || !result.DBAnchor.Equal(anchor) {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunCutoverProvisionOnlyRejectsBeforeT0AndAfterFinalize(t *testing.T) {
	anchor := time.Date(2026, 8, 12, 2, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		t0        time.Time
		finalized bool
		want      string
	}{
		{name: "before T0", t0: anchor.Add(time.Hour), want: "before activation T0"},
		{name: "after finalize", t0: anchor.Add(-25 * time.Hour), finalized: true, want: "already finalized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).
				WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
			mock.ExpectQuery(`FROM qa_lifecycle_receipts`).
				WillReturnRows(sqlmock.NewRows([]string{"activate_t0", "finalized"}).AddRow(tc.t0, tc.finalized))

			_, err = RunCutoverProvisionOnly(context.Background(), db, Options{HoursAhead: 1})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RunCutoverProvisionOnly() err=%v, want %q", err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDropExpiredHourLocksChildBeforeInspectingArchiveCoverage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	hour := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	child := pgpartition.ChildPartitionBound{
		Schema: "public", Name: "qa_records_20260810_10", Lower: hour, Upper: hour.Add(time.Hour),
	}

	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE "public"\."qa_records_20260810_10" IN ACCESS EXCLUSIVE MODE`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow(
		child.Schema, child.Name,
		"FOR VALUES FROM ('2026-08-10 10:00:00+00') TO ('2026-08-10 11:00:00+00')",
		false, false, false, child.Lower, child.Upper,
	))
	mock.ExpectQuery(`SELECT 1`).WillReturnRows(sqlmock.NewRows([]string{"sentinel"}).AddRow(1))
	mock.ExpectExec(`DROP TABLE IF EXISTS "public"\."qa_records_20260810_10"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	_, err = DropExpiredHour(
		context.Background(), conn, lockOrderingControl{}, child, hour.Add(25*time.Hour),
	)
	if err != nil {
		t.Fatalf("DropExpiredHour() err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDropExpiredHourPreservesUnknownCommitExistenceWhileDroppingSource(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	hour := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	child := pgpartition.ChildPartitionBound{
		Schema: "public", Name: "qa_records_20260810_10", Lower: hour, Upper: hour.Add(time.Hour),
	}
	control := &preserveUnknownControl{}

	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE "public"\."qa_records_20260810_10" IN ACCESS EXCLUSIVE MODE`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow(
		child.Schema, child.Name,
		"FOR VALUES FROM ('2026-08-10 10:00:00+00') TO ('2026-08-10 11:00:00+00')",
		false, false, false, child.Lower, child.Upper,
	))
	mock.ExpectExec(`DROP TABLE IF EXISTS "public"\."qa_records_20260810_10"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	result, err := DropExpiredHour(
		context.Background(), conn, control, child, hour.Add(25*time.Hour),
	)
	if err != nil {
		t.Fatalf("DropExpiredHour() err=%v", err)
	}
	if control.persistCalled || control.recordedID != 77 || control.recordedName != child.Name || result.TerminalGap {
		t.Fatalf("control=%+v result=%+v", control, result)
	}
	if result.SourceDroppedAt.IsZero() {
		t.Fatalf("source drop was not recorded: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDropExpiredHourRejectsCatalogBoundDriftAfterLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	hour := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	child := pgpartition.ChildPartitionBound{
		Schema: "public", Name: "qa_records_20260810_10", Lower: hour.Add(-time.Hour), Upper: hour,
	}

	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE "public"\."qa_records_20260810_10" IN ACCESS EXCLUSIVE MODE`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow(
		child.Schema, child.Name,
		"FOR VALUES FROM ('2026-08-10 10:00:00+00') TO ('2026-08-10 11:00:00+00')",
		false, false, false, hour, hour.Add(time.Hour),
	))
	mock.ExpectRollback()

	_, err = DropExpiredHour(
		context.Background(), conn, lockOrderingControl{}, child, hour.Add(25*time.Hour),
	)
	if err == nil || !strings.Contains(err.Error(), "catalog bound drift") {
		t.Fatalf("DropExpiredHour() err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

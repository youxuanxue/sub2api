//go:build unit

package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/lib/pq"
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

type missingArchiveControl struct {
	persistCalled bool
	persistedID   int64
	recordedID    int64
}

func TestDropFixedAgeHourRejectsNonHourlyPartitionBeforeDrop(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	hour := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	_, err = dropFixedAgeHour(
		context.Background(),
		conn,
		lockOrderingControl{},
		pgpartition.ChildPartitionBound{
			Schema: "public",
			Name:   "qa_records_legacy",
			Lower:  hour,
			Upper:  hour.Add(2 * time.Hour),
		},
		hour.Add(25*time.Hour),
		time.Now,
	)
	if err == nil || !strings.Contains(err.Error(), "not an exact UTC hour") {
		t.Fatalf("err=%v", err)
	}
}

func TestDropFixedAgeHourPersistsTerminalGapBeforeDrop(t *testing.T) {
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

	hour := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	child := pgpartition.ChildPartitionBound{
		Schema: "public", Name: "qa_records_20260814_01", Lower: hour, Upper: hour.Add(time.Hour),
	}
	expectTransitionPartitionDrop(mock, child)
	control := &missingArchiveControl{persistedID: 88}
	result, err := dropFixedAgeHour(
		context.Background(), conn, control, child, hour.Add(25*time.Hour), time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TerminalGap || !control.persistCalled || control.recordedID != 88 {
		t.Fatalf("result=%+v control=%+v", result, control)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDropFixedAgeHourRejectsMissingDurableGapIdentity(t *testing.T) {
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

	hour := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	child := pgpartition.ChildPartitionBound{
		Schema: "public", Name: "qa_records_20260814_01", Lower: hour, Upper: hour.Add(time.Hour),
	}
	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE "public"\."qa_records_20260814_01" IN ACCESS EXCLUSIVE MODE`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow(
		child.Schema, child.Name,
		"FOR VALUES FROM ('2026-08-14 01:00:00+00') TO ('2026-08-14 02:00:00+00')",
		false, false, false, child.Lower, child.Upper,
	))
	mock.ExpectRollback()

	_, err = dropFixedAgeHour(
		context.Background(), conn, &missingArchiveControl{}, child, hour.Add(25*time.Hour), time.Now,
	)
	if err == nil || !strings.Contains(err.Error(), "durable shard identity") {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDropFixedAgeHourPreservesUnknownCommitClassification(t *testing.T) {
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

	hour := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	child := pgpartition.ChildPartitionBound{
		Schema: "public", Name: "qa_records_20260814_01", Lower: hour, Upper: hour.Add(time.Hour),
	}
	expectTransitionPartitionDrop(mock, child)
	control := &preserveUnknownControl{}
	result, err := dropFixedAgeHour(
		context.Background(), conn, control, child, hour.Add(25*time.Hour), time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.TerminalGap || control.persistCalled || control.recordedID != 77 || control.recordedName != child.Name {
		t.Fatalf("result=%+v control=%+v", result, control)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectTransitionPartitionDrop(mock sqlmock.Sqlmock, child pgpartition.ChildPartitionBound) {
	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE "public"\."qa_records_20260814_01" IN ACCESS EXCLUSIVE MODE`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow(
		child.Schema, child.Name,
		"FOR VALUES FROM ('2026-08-14 01:00:00+00') TO ('2026-08-14 02:00:00+00')",
		false, false, false, child.Lower, child.Upper,
	))
	mock.ExpectExec(`DROP TABLE IF EXISTS "public"\."qa_records_20260814_01"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
}

func (*missingArchiveControl) InspectCatchupHourTx(context.Context, *sql.Tx, archive.Window) (archive.CatchupHourStatus, error) {
	return archive.CatchupHourStatus{}, nil
}

func (c *missingArchiveControl) PersistBoundaryTerminalGap(context.Context, *sql.Tx, archive.Window) (int64, error) {
	c.persistCalled = true
	return c.persistedID, nil
}

func (c *missingArchiveControl) RecordSourceDropped(_ context.Context, _ *sql.Tx, shardID int64, _ string, _ time.Time) error {
	c.recordedID = shardID
	return nil
}

func (*missingArchiveControl) RecordHotFilesCleaned(context.Context, *sql.Conn, int64, time.Time, string) error {
	return nil
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

func TestDefaultProvisionLockRetryBackoffCoversMinuteScaleContention(t *testing.T) {
	if len(qaProvisionLockRetryBackoff) < 8 {
		t.Fatalf("default lock-retry steps=%d; want >= 8", len(qaProvisionLockRetryBackoff))
	}
	var total time.Duration
	for _, delay := range qaProvisionLockRetryBackoff {
		if delay <= 0 {
			t.Fatalf("default lock-retry backoff contains non-positive delay: %v", qaProvisionLockRetryBackoff)
		}
		total += delay
	}
	if total < time.Minute {
		t.Fatalf(
			"default lock-retry backoff total=%s; want >= 1m to survive short DDL contention (issue #2446)",
			total,
		)
	}
}

func TestRunProvisionRetriesOnlyLockContentionBeforeCoverageCheck(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	anchor := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS`).
		WillReturnError(fmt.Errorf("wrapped create: %w", &pq.Error{Code: pq.ErrorCode("55P03")}))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS`).
		WillReturnError(errors.New("pq: canceling statement due to lock timeout"))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`WITH child_bounds AS`).
		WithArgs(TableQARecords, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	result, err := RunProvision(context.Background(), db, Options{
		HoursAhead:                1,
		provisionLockRetryBackoff: []time.Duration{0, 0},
	}, nil)
	if err != nil {
		t.Fatalf("RunProvision() err=%v", err)
	}
	if result.Attempts != 3 || result.LockRetries != 2 || result.RangesCovered != 1 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunProvisionDoesNotRetryNonLockFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	anchor := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS`).
		WillReturnError(&pq.Error{Code: pq.ErrorCode("53100")})

	result, err := RunProvision(context.Background(), db, Options{
		HoursAhead:                1,
		provisionLockRetryBackoff: []time.Duration{0, 0},
	}, nil)
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr == nil || string(pqErr.Code) != "53100" {
		t.Fatalf("RunProvision() err=%v", err)
	}
	if result.Attempts != 1 || result.LockRetries != 0 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunProvisionDoesNotRetryNearMatchLockTimeoutText(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	anchor := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS`).
		WillReturnError(errors.New("pq: canceling statement due to lock timeout; unrelated diagnostic"))

	result, err := RunProvision(context.Background(), db, Options{
		HoursAhead:                1,
		provisionLockRetryBackoff: []time.Duration{0},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "unrelated diagnostic") {
		t.Fatalf("RunProvision() err=%v", err)
	}
	if result.Attempts != 1 || result.LockRetries != 0 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunProvisionContextCancellationStopsLockRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	anchor := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT date_trunc\('hour', clock_timestamp\(\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"anchor"}).AddRow(anchor))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS`).
		WillReturnError(&pq.Error{Code: pq.ErrorCode("55P03")})

	ctx, cancel := context.WithCancel(context.Background())
	result, err := RunProvision(ctx, db, Options{
		HoursAhead:                1,
		provisionLockRetryBackoff: []time.Duration{time.Second},
		provisionRetrySleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		},
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunProvision() err=%v", err)
	}
	if result.Attempts != 1 || result.LockRetries != 0 || result.RangesCovered != 0 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

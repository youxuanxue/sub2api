//go:build unit

package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

type cleanupRecordingControl struct {
	shards []int64
	errors []string
}

func TestDropTransitionExpiredHourRejectsNonHourlyPartitionBeforeDrop(t *testing.T) {
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
	_, err = DropTransitionExpiredHour(
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

func TestDropTransitionExpiredHourPersistsTerminalGapBeforeDrop(t *testing.T) {
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
	result, err := DropTransitionExpiredHour(
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

func TestDropTransitionExpiredHourRejectsMissingDurableGapIdentity(t *testing.T) {
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

	_, err = DropTransitionExpiredHour(
		context.Background(), conn, &missingArchiveControl{}, child, hour.Add(25*time.Hour), time.Now,
	)
	if err == nil || !strings.Contains(err.Error(), "durable shard identity") {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDropTransitionExpiredHourPreservesUnknownCommitClassification(t *testing.T) {
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
	result, err := DropTransitionExpiredHour(
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

func (*cleanupRecordingControl) InspectCatchupHourTx(context.Context, *sql.Tx, archive.Window) (archive.CatchupHourStatus, error) {
	return archive.CatchupHourStatus{}, errors.New("unexpected archive inspection")
}

func (*cleanupRecordingControl) RecordSourceDropped(context.Context, *sql.Tx, int64, string, time.Time) error {
	return errors.New("unexpected source drop")
}

func (c *cleanupRecordingControl) RecordHotFilesCleaned(_ context.Context, _ *sql.Conn, shardID int64, _ time.Time, cleanupError string) error {
	c.shards = append(c.shards, shardID)
	c.errors = append(c.errors, cleanupError)
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

func TestDropCommittedHourValidatesSealBeforeAndAfterChildLock(t *testing.T) {
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
	hour := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	child := pgpartition.ChildPartitionBound{
		Schema: "public", Name: "qa_records_20260815_09", Lower: hour, Upper: hour.Add(time.Hour),
	}
	sealChecks := 0
	validateSeal := func() error {
		sealChecks++
		return nil
	}

	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE "public"\."qa_records_20260815_09" IN ACCESS EXCLUSIVE MODE`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow(
		child.Schema, child.Name,
		"FOR VALUES FROM ('2026-08-15 09:00:00+00') TO ('2026-08-15 10:00:00+00')",
		false, false, false, child.Lower, child.Upper,
	))
	mock.ExpectQuery(`SELECT 1`).WillReturnRows(sqlmock.NewRows([]string{"sentinel"}).AddRow(1))
	mock.ExpectExec(`DROP TABLE IF EXISTS "public"\."qa_records_20260815_09"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	result, err := DropCommittedHour(context.Background(), conn, lockOrderingControl{}, child, validateSeal, func() time.Time {
		return hour.Add(75 * time.Minute)
	})
	if err != nil {
		t.Fatalf("DropCommittedHour() err=%v", err)
	}
	if sealChecks != 2 || result.SourceDroppedAt.IsZero() {
		t.Fatalf("sealChecks=%d result=%+v", sealChecks, result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDropCommittedHourRollsBackWhenSealChangesAfterLock(t *testing.T) {
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
	hour := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	child := pgpartition.ChildPartitionBound{
		Schema: "public", Name: "qa_records_20260815_09", Lower: hour, Upper: hour.Add(time.Hour),
	}
	sealChecks := 0
	validateSeal := func() error {
		sealChecks++
		if sealChecks == 2 {
			return errors.New("seal digest changed")
		}
		return nil
	}

	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE "public"\."qa_records_20260815_09" IN ACCESS EXCLUSIVE MODE`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM pg_inherits`).WithArgs(TableQARecords).WillReturnRows(sqlmock.NewRows([]string{
		"schema", "name", "bound", "lower_unbounded", "upper_unbounded", "is_default", "lower", "upper",
	}).AddRow(
		child.Schema, child.Name,
		"FOR VALUES FROM ('2026-08-15 09:00:00+00') TO ('2026-08-15 10:00:00+00')",
		false, false, false, child.Lower, child.Upper,
	))
	mock.ExpectRollback()

	_, err = DropCommittedHour(context.Background(), conn, lockOrderingControl{}, child, validateSeal, time.Now)
	if err == nil || !strings.Contains(err.Error(), "seal digest changed") {
		t.Fatalf("DropCommittedHour() err=%v", err)
	}
	if sealChecks != 2 {
		t.Fatalf("sealChecks=%d", sealChecks)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResumePendingHotCleanupsCleansExactDroppedHoursAndIsIdempotent(t *testing.T) {
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

	root := t.TempDir()
	hour := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	for _, hotRoot := range []string{blobRootName, dlqRootName} {
		dir, pathErr := hourDir(root, hotRoot, hour)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		if pathErr := os.MkdirAll(dir, 0o700); pathErr != nil {
			t.Fatal(pathErr)
		}
		if pathErr := os.WriteFile(filepath.Join(dir, "evidence"), []byte("x"), 0o600); pathErr != nil {
			t.Fatal(pathErr)
		}
	}
	droppedAt := hour.Add(90 * time.Minute)
	mock.ExpectQuery(`SELECT id, window_start`).WithArgs(MaxPendingHotCleanup).
		WillReturnRows(sqlmock.NewRows([]string{"id", "window_start"}).AddRow(41, hour))
	mock.ExpectQuery(`SELECT id, source_partition_name, source_dropped_at, hot_files_cleaned_at`).WithArgs(hour).
		WillReturnRows(sqlmock.NewRows([]string{"id", "source_partition_name", "source_dropped_at", "hot_files_cleaned_at"}).
			AddRow(41, "qa_records_20260815_08", droppedAt, nil))
	control := &cleanupRecordingControl{}

	results, err := ResumePendingHotCleanups(context.Background(), conn, control, Options{BlobRoot: root, DLQRoot: root})
	if err != nil {
		t.Fatalf("ResumePendingHotCleanups() err=%v", err)
	}
	if len(results) != 1 || results[0].ShardID != 41 || !results[0].WindowStart.Equal(hour) || !results[0].Cleaned || results[0].Error != "" {
		t.Fatalf("results=%+v", results)
	}
	for _, hotRoot := range []string{blobRootName, dlqRootName} {
		dir, _ := hourDir(root, hotRoot, hour)
		if _, statErr := os.Lstat(dir); !os.IsNotExist(statErr) {
			t.Fatalf("exact hour directory still exists: %s err=%v", dir, statErr)
		}
	}
	if fmt.Sprint(control.shards) != "[41]" || len(control.errors) != 1 || control.errors[0] != "" {
		t.Fatalf("cleanup records shards=%v errors=%v", control.shards, control.errors)
	}

	mock.ExpectQuery(`SELECT id, window_start`).WithArgs(MaxPendingHotCleanup).
		WillReturnRows(sqlmock.NewRows([]string{"id", "window_start"}))
	results, err = ResumePendingHotCleanups(context.Background(), conn, control, Options{BlobRoot: root, DLQRoot: root})
	if err != nil || len(results) != 0 {
		t.Fatalf("idempotent resume results=%+v err=%v", results, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResumePendingHotCleanupsKeepsPerHourFailureVisibleAfterDrop(t *testing.T) {
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

	root := t.TempDir()
	failedHour := time.Date(2026, 8, 15, 6, 0, 0, 0, time.UTC)
	cleanHour := failedHour.Add(time.Hour)
	failedDir, err := hourDir(root, blobRootName, failedHour)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(failedDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), failedDir); err != nil {
		t.Fatal(err)
	}
	cleanDir, err := hourDir(root, blobRootName, cleanHour)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cleanDir, 0o700); err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery(`SELECT id, window_start`).WithArgs(MaxPendingHotCleanup).
		WillReturnRows(sqlmock.NewRows([]string{"id", "window_start"}).AddRow(51, failedHour).AddRow(52, cleanHour))
	for _, pending := range []struct {
		shardID int64
		hour    time.Time
	}{{51, failedHour}, {52, cleanHour}} {
		mock.ExpectQuery(`SELECT id, source_partition_name, source_dropped_at, hot_files_cleaned_at`).WithArgs(pending.hour).
			WillReturnRows(sqlmock.NewRows([]string{"id", "source_partition_name", "source_dropped_at", "hot_files_cleaned_at"}).
				AddRow(pending.shardID, fmt.Sprintf("qa_records_%s", pending.hour.Format("20060102_15")), pending.hour.Add(2*time.Hour), nil))
	}
	control := &cleanupRecordingControl{}
	results, err := ResumePendingHotCleanups(context.Background(), conn, control, Options{BlobRoot: root, DLQRoot: root})
	if err != nil {
		t.Fatalf("ResumePendingHotCleanups() err=%v", err)
	}
	if len(results) != 2 || results[0].Cleaned || !strings.Contains(results[0].Error, "symlink") || !results[1].Cleaned || results[1].Error != "" {
		t.Fatalf("results=%+v", results)
	}
	if _, statErr := os.Lstat(failedDir); statErr != nil {
		t.Fatalf("failed cleanup must leave exact hour visible: %v", statErr)
	}
	if fmt.Sprint(control.shards) != "[51 52]" || len(control.errors) != 2 || !strings.Contains(control.errors[0], "symlink") || control.errors[1] != "" {
		t.Fatalf("cleanup records shards=%v errors=%v", control.shards, control.errors)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

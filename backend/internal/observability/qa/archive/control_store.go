package archive

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Window is one exact UTC source hour [Start, End).
type Window struct {
	Start time.Time
	End   time.Time
}

// SQLControlStore owns the generation-0 qa_archive_shards rows that the local
// 24h cleanup uses as its durable drop and hot-file cleanup receipt.
type SQLControlStore struct{}

func NewSQLControlStore() *SQLControlStore { return &SQLControlStore{} }

func (s *SQLControlStore) InspectCatchupHourTx(ctx context.Context, tx *sql.Tx, window Window) (CatchupHourStatus, error) {
	return inspectCatchupHour(ctx, tx, window)
}

func inspectCatchupHour(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, window Window) (CatchupHourStatus, error) {
	if querier == nil {
		return CatchupHourStatus{}, fmt.Errorf("inspect catchup hour: nil querier")
	}
	window.Start = window.Start.UTC()
	window.End = window.End.UTC()
	if !window.End.Equal(window.Start.Add(time.Hour)) {
		return CatchupHourStatus{}, fmt.Errorf("inspect catchup hour: window must be one UTC hour")
	}

	var status CatchupHourStatus
	var storedEnd sql.NullTime
	err := querier.QueryRowContext(ctx, `
SELECT
    s.id IS NOT NULL,
    COALESCE(s.id, 0),
    s.window_end,
    COALESCE(s.state, ''),
    s.restore_verified_at IS NOT NULL,
    COALESCE(s.verification_error_code, ''),
    EXISTS (
        SELECT 1
        FROM qa_records q
        WHERE q.created_at >= $1 AND q.created_at < $2
    )
FROM (SELECT 1) AS anchor
LEFT JOIN qa_archive_shards s
  ON s.window_start = $1 AND s.generation = 0`, window.Start, window.End).Scan(
		&status.Exists,
		&status.ShardID,
		&storedEnd,
		&status.State,
		&status.RestoreVerified,
		&status.VerificationErrorCode,
		&status.SourceExists,
	)
	if err != nil {
		return CatchupHourStatus{}, fmt.Errorf("inspect catchup hour: %w", err)
	}
	// QA S3 is retired (2026-11-08): no segment membership is ever recorded
	// again, so every remaining source row in the hour is uncovered.
	status.UncoveredSourceExists = status.SourceExists
	if status.Exists && (!storedEnd.Valid || !storedEnd.Time.UTC().Equal(window.End)) {
		return CatchupHourStatus{}, fmt.Errorf("inspect catchup hour: stored window end does not match")
	}
	return status, nil
}

func (s *SQLControlStore) PersistBoundaryTerminalGap(ctx context.Context, tx *sql.Tx, window Window) (int64, error) {
	window.Start = window.Start.UTC()
	window.End = window.End.UTC()
	if !window.End.Equal(window.Start.Add(time.Hour)) {
		return 0, fmt.Errorf("boundary terminal gap: window must be one UTC hour")
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO qa_archive_shards (
    window_start, window_end, generation, state, s3_prefix, first_attempt_at,
    cleanup_eligible, created_at, updated_at
) VALUES (
    $1, $2, 0, $3, $4, now(), false, now(), now()
)
ON CONFLICT (window_start, generation) DO NOTHING`,
		window.Start, window.End, StatePending, ShardPrefix(window.Start),
	)
	if err != nil {
		return 0, fmt.Errorf("boundary terminal gap: insert control: %w", err)
	}
	var shardID int64
	var state string
	var code sql.NullString
	var restoreVerified sql.NullTime
	err = tx.QueryRowContext(ctx, `
SELECT id, state, verification_error_code, restore_verified_at
FROM qa_archive_shards
WHERE window_start=$1 AND generation=0
FOR UPDATE`, window.Start).Scan(&shardID, &state, &code, &restoreVerified)
	if err != nil {
		return 0, fmt.Errorf("boundary terminal gap: lock control: %w", err)
	}
	if state == StateFailed && code.String == IntegritySourceUnavailableAfterRetention {
		return shardID, nil
	}
	if state == StateFailed && code.String == IntegrityCommitExistenceUnknown {
		return shardID, nil
	}
	if state == StateCommitted && restoreVerified.Valid && code.String == "" {
		var uncovered bool
		if err := tx.QueryRowContext(ctx, uncoveredSourceInWindowQuery, window.Start, window.End).Scan(&uncovered); err != nil {
			return 0, fmt.Errorf("boundary terminal gap: inspect uncovered membership: %w", err)
		}
		if !uncovered {
			return shardID, nil
		}
	}
	if state == StateFailed && IsTerminalArchiveFailure(code.String) {
		if code.String != IntegritySourceUnavailableAfterRetention {
			return shardID, nil
		}
	}
	result, err := tx.ExecContext(ctx, `
UPDATE qa_archive_shards SET
    state=$1,
    verification_error_code=$2,
    last_error='source unavailable after retention at boundary drop',
    last_reconciled_at=now(),
    cleanup_eligible=false,
    updated_at=now()
WHERE id=$3 AND state IN ('pending', 'writing', 'verified', 'committed', 'failed')`,
		StateFailed, IntegritySourceUnavailableAfterRetention, shardID,
	)
	if err != nil {
		return 0, fmt.Errorf("boundary terminal gap: update control: %w", err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil {
		return 0, fmt.Errorf("boundary terminal gap: inspect update: %w", rowsErr)
	} else if changed != 1 && (state != StateFailed || code.String != IntegritySourceUnavailableAfterRetention) {
		return 0, fmt.Errorf("boundary terminal gap: shard changed concurrently")
	}
	return shardID, nil
}

// RecordSourceDropped binds the dropped partition name to the shard control row.
func (s *SQLControlStore) RecordSourceDropped(ctx context.Context, tx *sql.Tx, shardID int64, partitionName string, droppedAt time.Time) error {
	if shardID <= 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
UPDATE qa_archive_shards SET
    source_partition_name=$2,
    source_dropped_at=$3,
    updated_at=now()
WHERE id=$1`, shardID, partitionName, droppedAt.UTC())
	return err
}

// RecordHotFilesCleaned records post-DROP hot-file cleanup completion or a redacted error.
func (s *SQLControlStore) RecordHotFilesCleaned(ctx context.Context, execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, shardID int64, cleanedAt time.Time, cleanupError string) error {
	if shardID <= 0 {
		return nil
	}
	if strings.TrimSpace(cleanupError) == "" {
		_, err := execer.ExecContext(ctx, `
UPDATE qa_archive_shards SET
    hot_files_cleaned_at=$2,
    hot_cleanup_error=NULL,
    updated_at=now()
WHERE id=$1`, shardID, cleanedAt.UTC())
		return err
	}
	_, err := execer.ExecContext(ctx, `
UPDATE qa_archive_shards SET
    hot_cleanup_error=$2,
    updated_at=now()
WHERE id=$1 AND hot_files_cleaned_at IS NULL`, shardID, redactHotCleanupError(cleanupError))
	return err
}

// uncoveredSourceInWindowQuery reports whether source rows remain in the hour.
// With QA S3 retired there is no archived membership to subtract.
const uncoveredSourceInWindowQuery = `
SELECT EXISTS (
    SELECT 1
    FROM qa_records q
    WHERE q.created_at >= $1 AND q.created_at < $2
)`

func redactHotCleanupError(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > 512 {
		msg = msg[:512]
	}
	return msg
}

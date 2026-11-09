// Package lifecycle owns the QA UTC-hour boundary phase: provisioning, expiry DROP,
// and matching hot-file cleanup. Archive reconciliation remains in archive package.
package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/lib/pq"
)

const (
	TableQARecords       = "qa_records"
	HourlyHorizon        = pgpartition.QARecordsHourlyHorizon
	MaintenanceLockID    = archive.MaintenanceAdvisoryLockID
	MaxPendingHotCleanup = 48
	MaxTransitionDrops   = 48

	qaProvisionLockContentionSQLState = "55P03"
)

// qaProvisionLockRetryBackoff budgets roughly one minute of CREATE TABLE
// lock_timeout (100ms) contention. Prod 2026-10-06 08:00 UTC exhausted the
// previous six-step ~16s budget on qa_records_20261009_07 and recovered on the
// next hour; keep the geometric ramp and add two longer plateaus.
var qaProvisionLockRetryBackoff = [...]time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
}

type DB interface {
	pgpartition.DB
}

type Clock func(context.Context, DB) (time.Time, error)

type ControlStore interface {
	InspectCatchupHourTx(context.Context, *sql.Tx, archive.Window) (archive.CatchupHourStatus, error)
	RecordSourceDropped(context.Context, *sql.Tx, int64, string, time.Time) error
	RecordHotFilesCleaned(context.Context, *sql.Conn, int64, time.Time, string) error
}

type TransitionControlStore interface {
	ControlStore
	PersistBoundaryTerminalGap(context.Context, *sql.Tx, archive.Window) (int64, error)
}

type ProvisionResult struct {
	DBAnchor       time.Time `json:"db_anchor_utc"`
	HoursAhead     int       `json:"hours_ahead"`
	RangesRequired int       `json:"ranges_required"`
	RangesCovered  int       `json:"ranges_covered"`
	Attempts       int       `json:"attempts"`
	LockRetries    int       `json:"lock_retries"`
	Error          string    `json:"error,omitempty"`
}

type ExpiryResult struct {
	DBAnchor          time.Time `json:"db_anchor_utc"`
	RetentionBoundary time.Time `json:"retention_boundary_utc"`
	PartitionName     string    `json:"partition_name,omitempty"`
	PartitionUpper    time.Time `json:"partition_upper_utc,omitempty"`
	TerminalGap       bool      `json:"terminal_gap"`
	SourceDroppedAt   time.Time `json:"source_dropped_at,omitempty"`
	HotFilesCleaned   bool      `json:"hot_files_cleaned"`
	Error             string    `json:"error,omitempty"`
}

type Options struct {
	HoursAhead int
	BlobRoot   string
	DLQRoot    string
	// ArchiveReceiptRoot is the local, read-only view of node1 receipts. When
	// set, a source hour is never dropped until its verified receipt exists.
	// An unavailable or malformed receipt is a hard safety failure.
	ArchiveReceiptRoot string
	ArchiveEnvironment string

	provisionLockRetryBackoff []time.Duration
	provisionRetrySleep       func(context.Context, time.Duration) error
}

func DatabaseUTC(ctx context.Context, db DB) (time.Time, error) {
	var anchor time.Time
	if err := db.QueryRowContext(ctx, `SELECT date_trunc('hour', clock_timestamp())`).Scan(&anchor); err != nil {
		return time.Time{}, fmt.Errorf("lifecycle: read database UTC anchor: %w", err)
	}
	return anchor.UTC(), nil
}

func sleepForProvisionRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isProvisionLockContention(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr != nil && string(pqErr.Code) == qaProvisionLockContentionSQLState
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		message := strings.TrimSpace(strings.ToLower(current.Error()))
		if message == "pq: canceling statement due to lock timeout" ||
			message == "canceling statement due to lock timeout" {
			return true
		}
	}
	return false
}

// RunProvision ensures [anchor, anchor+hoursAhead) hourly children exist.
func RunProvision(ctx context.Context, db DB, opts Options, clock Clock) (ProvisionResult, error) {
	result := ProvisionResult{HoursAhead: opts.HoursAhead}
	if opts.HoursAhead <= 0 {
		opts.HoursAhead = HourlyHorizon
		result.HoursAhead = opts.HoursAhead
	}
	if clock == nil {
		clock = DatabaseUTC
	}
	anchor, err := clock(ctx, db)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	result.DBAnchor = anchor
	ranges := pgpartition.HourlyTargetRanges(anchor, opts.HoursAhead)
	result.RangesRequired = len(ranges)
	backoff := opts.provisionLockRetryBackoff
	if backoff == nil {
		backoff = qaProvisionLockRetryBackoff[:]
	}
	retrySleep := opts.provisionRetrySleep
	if retrySleep == nil {
		retrySleep = sleepForProvisionRetry
	}
	for {
		result.Attempts++
		err = pgpartition.EnsureHourly(ctx, db, TableQARecords, anchor, opts.HoursAhead)
		if err == nil {
			break
		}
		if !isProvisionLockContention(err) || result.LockRetries >= len(backoff) {
			result.Error = err.Error()
			return result, err
		}
		if sleepErr := retrySleep(ctx, backoff[result.LockRetries]); sleepErr != nil {
			err = fmt.Errorf("lifecycle: wait to retry hourly partition lock contention: %w", sleepErr)
			result.Error = err.Error()
			return result, err
		}
		result.LockRetries++
	}
	covered, err := pgpartition.CountCoveredHourlyRanges(ctx, db, TableQARecords, ranges)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	result.RangesCovered = covered
	if covered != len(ranges) {
		err = fmt.Errorf("lifecycle: qa_records covers %d of %d required hourly ranges", covered, len(ranges))
		result.Error = err.Error()
		return result, err
	}
	return result, nil
}

func revalidateLockedChild(
	ctx context.Context,
	tx *sql.Tx,
	expected pgpartition.ChildPartitionBound,
	boundary time.Time,
) (pgpartition.ChildPartitionBound, error) {
	children, err := pgpartition.ListChildPartitionBounds(ctx, tx, TableQARecords)
	if err != nil {
		return pgpartition.ChildPartitionBound{}, fmt.Errorf("lifecycle: revalidate locked partition: %w", err)
	}
	for _, current := range children {
		if current.Schema != expected.Schema || current.Name != expected.Name {
			continue
		}
		if current.IsDefault || !current.Lower.Equal(expected.Lower) || !current.Upper.Equal(expected.Upper) {
			return pgpartition.ChildPartitionBound{}, fmt.Errorf(
				"lifecycle: locked partition %s.%s catalog bound drift",
				expected.Schema,
				expected.Name,
			)
		}
		if current.Upper.After(boundary) {
			return pgpartition.ChildPartitionBound{}, fmt.Errorf(
				"lifecycle: locked partition %s.%s is no longer expired",
				expected.Schema,
				expected.Name,
			)
		}
		return current, nil
	}
	return pgpartition.ChildPartitionBound{}, fmt.Errorf(
		"lifecycle: locked partition %s.%s is no longer attached to %s",
		expected.Schema,
		expected.Name,
		TableQARecords,
	)
}

// SelectTransitionExpiredHours returns the fixed-age hours owned by the
// pre-activation boundary during ledger warm-up.
func SelectTransitionExpiredHours(ctx context.Context, db DB, boundary time.Time) ([]pgpartition.ChildPartitionBound, error) {
	children, err := pgpartition.ListChildPartitionBounds(ctx, db, TableQARecords)
	if err != nil {
		return nil, err
	}
	var expired []pgpartition.ChildPartitionBound
	for _, child := range children {
		if child.IsDefault || child.Upper.After(boundary) {
			continue
		}
		expired = append(expired, child)
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i].Upper.Before(expired[j].Upper) })
	return expired, nil
}

func transitionNeedsTerminalGap(status archive.CatchupHourStatus) bool {
	if !status.Exists {
		return true
	}
	if status.State == archive.StateFailed && status.VerificationErrorCode == archive.IntegrityCommitExistenceUnknown {
		return false
	}
	if status.State != archive.StateCommitted || !status.RestoreVerified {
		return true
	}
	return status.UncoveredSourceExists
}

// dropFixedAgeHour is the explicitly authorized local 24h retention drop. It
// persists a terminal-gap receipt before DROP when the hour was never archived.
func dropFixedAgeHour(ctx context.Context, conn *sql.Conn, control TransitionControlStore,
	child pgpartition.ChildPartitionBound, boundary time.Time, now func() time.Time,
) (ExpiryResult, error) {
	result := ExpiryResult{RetentionBoundary: boundary, PartitionName: child.Name, PartitionUpper: child.Upper}
	if control == nil {
		return result, errors.New("lifecycle: transition archive control is required")
	}
	if now == nil {
		now = time.Now
	}
	window := archive.Window{Start: child.Lower.UTC(), End: child.Upper.UTC()}
	if !window.End.Equal(window.Start.Add(time.Hour)) {
		return result, errors.New("lifecycle: source partition is not an exact UTC hour")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := pgpartition.LockChildPartition(ctx, tx, child); err != nil {
		result.Error = err.Error()
		return result, err
	}
	child, err = revalidateLockedChild(ctx, tx, child, boundary)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	status, err := control.InspectCatchupHourTx(ctx, tx, window)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	shardID := status.ShardID
	if transitionNeedsTerminalGap(status) {
		shardID, err = control.PersistBoundaryTerminalGap(ctx, tx, window)
		if err != nil {
			result.Error = err.Error()
			return result, fmt.Errorf("lifecycle: transition terminal gap classification: %w", err)
		}
		result.TerminalGap = true
	}
	if shardID <= 0 {
		err = errors.New("lifecycle: transition drop requires a durable shard identity")
		result.Error = err.Error()
		return result, err
	}
	if err := pgpartition.DropChildPartition(ctx, tx, child); err != nil {
		result.Error = err.Error()
		return result, err
	}
	droppedAt := now().UTC()
	if err := control.RecordSourceDropped(ctx, tx, shardID, child.Name, droppedAt); err != nil {
		result.Error = err.Error()
		return result, fmt.Errorf("lifecycle: record transition source drop: %w", err)
	}
	if err := tx.Commit(); err != nil {
		result.Error = err.Error()
		return result, fmt.Errorf("lifecycle: commit transition partition drop: %w", err)
	}
	result.SourceDroppedAt = droppedAt
	return result, nil
}

// ResumeDroppedHourCleanup uses the durable archive-control drop receipt to
// finish an exact hour whose partition was already removed in a prior run.
func ResumeDroppedHourCleanup(
	ctx context.Context,
	conn *sql.Conn,
	control ControlStore,
	hourStart time.Time,
	opts Options,
) (ExpiryResult, error) {
	result := ExpiryResult{
		RetentionBoundary: hourStart.UTC().Add(time.Hour),
		PartitionUpper:    hourStart.UTC().Add(time.Hour),
	}
	if control == nil {
		return result, errors.New("lifecycle: archive control is required")
	}
	var shardID int64
	var partitionName sql.NullString
	var sourceDroppedAt sql.NullTime
	var hotFilesCleanedAt sql.NullTime
	err := conn.QueryRowContext(ctx, `
SELECT id, source_partition_name, source_dropped_at, hot_files_cleaned_at
FROM qa_archive_shards
WHERE window_start = $1 AND generation = 0`, hourStart.UTC()).Scan(
		&shardID,
		&partitionName,
		&sourceDroppedAt,
		&hotFilesCleanedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return result, fmt.Errorf("lifecycle: no archive control for dropped hour %s", hourStart.UTC().Format(time.RFC3339))
		}
		return result, err
	}
	if !sourceDroppedAt.Valid {
		return result, fmt.Errorf("lifecycle: source hour %s was not durably dropped", hourStart.UTC().Format(time.RFC3339))
	}
	result.PartitionName = partitionName.String
	result.SourceDroppedAt = sourceDroppedAt.Time.UTC()
	if hotFilesCleanedAt.Valid {
		result.HotFilesCleaned = true
		return result, nil
	}
	cleanupErr := ""
	if err := RemoveHourDirectory(opts.BlobRoot, blobRootName, hourStart); err != nil {
		cleanupErr = redactCleanupError(err)
	}
	if err := RemoveHourDirectory(opts.DLQRoot, dlqRootName, hourStart); err != nil && cleanupErr == "" {
		cleanupErr = redactCleanupError(err)
	}
	if recordErr := control.RecordHotFilesCleaned(ctx, conn, shardID, time.Now().UTC(), cleanupErr); recordErr != nil {
		return result, recordErr
	}
	if cleanupErr != "" {
		result.Error = cleanupErr
		return result, fmt.Errorf("lifecycle: hot file cleanup: %s", cleanupErr)
	}
	result.HotFilesCleaned = true
	return result, nil
}

func redactCleanupError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 512 {
		msg = msg[:512]
	}
	return msg
}

// RetentionUntilForHour supplies the required legacy schema field for hourly writes.
// It is the exact source-hour upper bound and never authorizes deletion.
func RetentionUntilForHour(hourStart time.Time) time.Time {
	return pgpartition.HourStartUTC(hourStart).Add(time.Hour)
}

// UsesHourlyStorage reports whether createdAt is at or after the configured cutover hour.
func UsesHourlyStorage(cutover time.Time, createdAt time.Time) bool {
	if cutover.IsZero() {
		return false
	}
	return !createdAt.Before(cutover)
}

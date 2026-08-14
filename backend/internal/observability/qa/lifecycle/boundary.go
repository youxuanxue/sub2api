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
	TableQARecords        = "qa_records"
	HourlyHorizon         = pgpartition.QARecordsHourlyHorizon
	MaintenanceLockID     = archive.MaintenanceAdvisoryLockID
	MaxExpiredDropsPerRun = 48
	MaxHotCleanupResume   = 48

	qaProvisionLockContentionSQLState = "55P03"
)

var qaProvisionLockRetryBackoff = [...]time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
}

type DB interface {
	pgpartition.DB
}

type Clock func(context.Context, DB) (time.Time, error)

type ControlStore interface {
	InspectCatchupHourTx(context.Context, *sql.Tx, archive.Window) (archive.CatchupHourStatus, error)
	PersistBoundaryTerminalGap(context.Context, *sql.Tx, archive.Window) (int64, error)
	RecordSourceDropped(context.Context, *sql.Tx, int64, string, time.Time) error
	RecordHotFilesCleaned(context.Context, *sql.Conn, int64, time.Time, string) error
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

type HotCleanupResumeResult struct {
	ShardID     int64     `json:"shard_id"`
	WindowStart time.Time `json:"window_start_utc"`
	Cleaned     bool      `json:"cleaned"`
	Error       string    `json:"error,omitempty"`
}

type BoundaryResult struct {
	Provision          ProvisionResult          `json:"provision"`
	Expiries           []ExpiryResult           `json:"expiries,omitempty"`
	Expiry             *ExpiryResult            `json:"expiry,omitempty"`
	HotCleanupResumed  []HotCleanupResumeResult `json:"hot_cleanup_resumed,omitempty"`
	DeletionAuthorized bool                     `json:"deletion_authorized"`
}

type Options struct {
	HoursAhead int
	BlobRoot   string
	DLQRoot    string

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

// RunCutoverProvisionOnly extends the hourly horizon after T0 without running expiry or cleanup.
func RunCutoverProvisionOnly(ctx context.Context, db DB, opts Options) (ProvisionResult, error) {
	anchor, err := DatabaseUTC(ctx, db)
	if err != nil {
		return ProvisionResult{}, err
	}
	var activateT0 sql.NullTime
	var finalized bool
	if err := db.QueryRowContext(ctx, `
SELECT
  (SELECT t0_utc FROM qa_lifecycle_receipts WHERE phase = 'activate'),
  EXISTS (SELECT 1 FROM qa_lifecycle_receipts WHERE phase = 'finalize')`).Scan(
		&activateT0, &finalized,
	); err != nil {
		return ProvisionResult{}, fmt.Errorf("lifecycle: read cutover receipts for provision-only: %w", err)
	}
	if !activateT0.Valid {
		return ProvisionResult{}, fmt.Errorf("lifecycle: provision-only requires an activation receipt")
	}
	if finalized {
		return ProvisionResult{}, fmt.Errorf("lifecycle: cutover is already finalized; boundary owns provisioning")
	}
	t0 := activateT0.Time.UTC()
	if anchor.Before(t0) {
		return ProvisionResult{}, fmt.Errorf("lifecycle: provision-only is forbidden before activation T0")
	}
	return RunProvision(ctx, db, opts, func(context.Context, DB) (time.Time, error) {
		return anchor, nil
	})
}

// SelectExpiredHours returns all direct hourly children whose catalog upper bound is at or before boundary.
func SelectExpiredHours(ctx context.Context, db DB, boundary time.Time) ([]pgpartition.ChildPartitionBound, error) {
	children, err := pgpartition.ListChildPartitionBounds(ctx, db, TableQARecords)
	if err != nil {
		return nil, err
	}
	var out []pgpartition.ChildPartitionBound
	for _, child := range children {
		if child.IsDefault {
			continue
		}
		if child.Upper.After(boundary) {
			continue
		}
		out = append(out, child)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Upper.Before(out[j].Upper)
	})
	return out, nil
}

func needsBoundaryTerminalGap(status archive.CatchupHourStatus) bool {
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

// DropExpiredHour drops one expired hourly child after optional terminal gap classification.
func DropExpiredHour(
	ctx context.Context,
	conn *sql.Conn,
	control ControlStore,
	child pgpartition.ChildPartitionBound,
	boundary time.Time,
) (ExpiryResult, error) {
	result := ExpiryResult{
		RetentionBoundary: boundary,
		PartitionName:     child.Name,
		PartitionUpper:    child.Upper,
	}
	window := archive.Window{Start: child.Lower, End: child.Upper}
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
	lockedChild, err := revalidateLockedChild(ctx, tx, child, boundary)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	child = lockedChild
	result.PartitionName = child.Name
	result.PartitionUpper = child.Upper

	status, err := control.InspectCatchupHourTx(ctx, tx, window)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	shardID := status.ShardID
	if needsBoundaryTerminalGap(status) {
		shardID, err = control.PersistBoundaryTerminalGap(ctx, tx, window)
		if err != nil {
			result.Error = err.Error()
			return result, fmt.Errorf("lifecycle: terminal gap classification: %w", err)
		}
		result.TerminalGap = true
	}
	if err := pgpartition.DropChildPartition(ctx, tx, child); err != nil {
		result.Error = err.Error()
		return result, fmt.Errorf("lifecycle: drop partition %s: %w", child.Name, err)
	}
	droppedAt := time.Now().UTC()
	if err := control.RecordSourceDropped(ctx, tx, shardID, child.Name, droppedAt); err != nil {
		result.Error = err.Error()
		return result, fmt.Errorf("lifecycle: record source drop: %w", err)
	}
	if err := tx.Commit(); err != nil {
		result.Error = err.Error()
		return result, fmt.Errorf("lifecycle: commit partition drop: %w", err)
	}
	result.SourceDroppedAt = droppedAt
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

func runExpiryDrops(
	ctx context.Context,
	db *sql.DB,
	control ControlStore,
	opts Options,
	anchor time.Time,
) ([]ExpiryResult, bool, error) {
	boundary := pgpartition.RetentionBoundary(anchor)
	expired, err := SelectExpiredHours(ctx, db, boundary)
	if err != nil {
		return nil, false, err
	}
	if len(expired) == 0 {
		return nil, false, nil
	}
	if len(expired) > MaxExpiredDropsPerRun {
		expired = expired[:MaxExpiredDropsPerRun]
	}
	out := make([]ExpiryResult, 0, len(expired))
	anyDropped := false
	var dropErr error
	for _, child := range expired {
		conn, err := db.Conn(ctx)
		if err != nil {
			return out, anyDropped, err
		}
		expiry, err := DropExpiredHour(ctx, conn, control, child, boundary)
		_ = conn.Close()
		expiry.DBAnchor = anchor
		out = append(out, expiry)
		if err != nil {
			dropErr = errors.Join(dropErr, err)
			continue
		}
		anyDropped = true
		if opts.BlobRoot != "" && !child.Lower.IsZero() {
			cleanupErr := cleanupHotFilesForShard(ctx, db, control, child.Lower, opts)
			if cleanupErr != nil {
				out[len(out)-1].Error = cleanupErr.Error()
				dropErr = errors.Join(dropErr, cleanupErr)
				continue
			}
			out[len(out)-1].HotFilesCleaned = true
		}
	}
	return out, anyDropped, dropErr
}

func cleanupHotFilesForShard(
	ctx context.Context,
	db *sql.DB,
	control ControlStore,
	hourStart time.Time,
	opts Options,
) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	window := archive.Window{Start: hourStart, End: hourStart.Add(time.Hour)}
	var shardID int64
	err = conn.QueryRowContext(ctx, `
SELECT id FROM qa_archive_shards WHERE window_start = $1 AND generation = 0`, window.Start).Scan(&shardID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	cleanupErr := ""
	if err := RemoveHourDirectory(opts.BlobRoot, blobRootName, hourStart); err != nil {
		cleanupErr = redactCleanupError(err)
	}
	if err := RemoveHourDirectory(opts.DLQRoot, dlqRootName, hourStart); err != nil && cleanupErr == "" {
		cleanupErr = redactCleanupError(err)
	}
	if recordErr := control.RecordHotFilesCleaned(ctx, conn, shardID, time.Now().UTC(), cleanupErr); recordErr != nil {
		return recordErr
	}
	if cleanupErr != "" {
		return fmt.Errorf("lifecycle: hot file cleanup: %s", cleanupErr)
	}
	return nil
}

func resumePendingHotCleanups(
	ctx context.Context,
	db *sql.DB,
	control ControlStore,
	opts Options,
) ([]HotCleanupResumeResult, error) {
	if opts.BlobRoot == "" {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, window_start
FROM qa_archive_shards
WHERE source_dropped_at IS NOT NULL
  AND hot_files_cleaned_at IS NULL
ORDER BY window_start
LIMIT $1`, MaxHotCleanupResume)
	if err != nil {
		return nil, fmt.Errorf("lifecycle: list pending hot cleanup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []HotCleanupResumeResult
	for rows.Next() {
		var shardID int64
		var windowStart time.Time
		if err := rows.Scan(&shardID, &windowStart); err != nil {
			return out, err
		}
		item := HotCleanupResumeResult{ShardID: shardID, WindowStart: windowStart.UTC()}
		cleanupErr := cleanupHotFilesForShard(ctx, db, control, windowStart, opts)
		if cleanupErr != nil {
			item.Error = cleanupErr.Error()
		} else {
			item.Cleaned = true
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// RunBoundary executes provision, bounded expiry DROP, and hot-file cleanup resume independently.
func RunBoundary(ctx context.Context, db *sql.DB, control ControlStore, opts Options) (BoundaryResult, error) {
	var out BoundaryResult
	var joined error

	provision, provErr := RunProvision(ctx, db, opts, nil)
	out.Provision = provision
	if provErr != nil {
		return out, provErr
	}

	anchor := provision.DBAnchor
	if anchor.IsZero() {
		var err error
		anchor, err = DatabaseUTC(ctx, db)
		if err != nil {
			return out, errors.Join(joined, err)
		}
	}

	expiries, anyDropped, expErr := runExpiryDrops(ctx, db, control, opts, anchor)
	out.Expiries = expiries
	if len(expiries) > 0 {
		last := expiries[len(expiries)-1]
		out.Expiry = &last
	}
	out.DeletionAuthorized = anyDropped
	if expErr != nil {
		joined = errors.Join(joined, expErr)
	}

	resumed, resumeErr := resumePendingHotCleanups(ctx, db, control, opts)
	out.HotCleanupResumed = resumed
	if resumeErr != nil {
		joined = errors.Join(joined, resumeErr)
	}
	for _, item := range resumed {
		if item.Error != "" {
			joined = errors.Join(joined, fmt.Errorf("lifecycle: resume hot cleanup shard %d: %s", item.ShardID, item.Error))
		}
	}

	return out, joined
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

// RetentionUntilForHour returns the partition upper bound plus 24h.
func RetentionUntilForHour(hourStart time.Time) time.Time {
	return pgpartition.HourStartUTC(hourStart).Add(25 * time.Hour)
}

// UsesHourlyStorage reports whether createdAt is at or after the configured cutover hour.
func UsesHourlyStorage(cutover time.Time, createdAt time.Time) bool {
	if cutover.IsZero() {
		return false
	}
	return !createdAt.Before(cutover)
}

// InventoryRow summarizes one child partition for cutover planning.
type InventoryRow struct {
	Schema    string    `json:"schema"`
	Name      string    `json:"name"`
	Lower     time.Time `json:"lower_utc"`
	Upper     time.Time `json:"upper_utc"`
	IsDefault bool      `json:"is_default"`
	RowCount  int64     `json:"row_count"`
	Layout    string    `json:"layout"`
}

// Inventory builds a read-only partition inventory.
func Inventory(ctx context.Context, db DB) ([]InventoryRow, error) {
	children, err := pgpartition.ListInventoryChildBounds(ctx, db, TableQARecords)
	if err != nil {
		return nil, err
	}
	out := make([]InventoryRow, 0, len(children))
	for _, child := range children {
		row := InventoryRow{
			Schema:    child.Schema,
			Name:      child.Name,
			IsDefault: child.IsDefault,
			Layout:    child.Layout,
		}
		if !child.IsDefault {
			row.Lower = child.Lower
			row.Upper = child.Upper
		}
		count, err := pgpartition.CountTableRows(ctx, db, child.Schema, child.Name)
		if err != nil {
			return nil, err
		}
		row.RowCount = count
		out = append(out, row)
	}
	return out, nil
}

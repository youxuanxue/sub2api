// Package lifecycle owns the QA UTC-hour boundary phase: provisioning, expiry DROP,
// and matching hot-file cleanup. Archive reconciliation remains in archive package.
package lifecycle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/lib/pq"
)

const (
	TableQARecords      = "qa_records"
	TimeColumnCreatedAt = "created_at"
	HourlyHorizon       = pgpartition.QARecordsHourlyHorizon
	MaintenanceLockID   = archive.MaintenanceAdvisoryLockID
)

type DB interface {
	pgpartition.DB
}

type Clock func(context.Context, DB) (time.Time, error)

type ControlStore interface {
	PersistBoundaryTerminalGap(context.Context, *sql.Tx, archive.Window) (int64, error)
	RecordSourceDropped(context.Context, *sql.Tx, int64, string, time.Time) error
	RecordHotFilesCleaned(context.Context, *sql.Conn, int64, time.Time, string) error
}

type ProvisionResult struct {
	DBAnchor       time.Time `json:"db_anchor_utc"`
	HoursAhead     int       `json:"hours_ahead"`
	RangesRequired int       `json:"ranges_required"`
	RangesCovered  int       `json:"ranges_covered"`
	Error          string    `json:"error,omitempty"`
}

type ExpiryResult struct {
	DBAnchor          time.Time `json:"db_anchor_utc"`
	RetentionBoundary time.Time `json:"retention_boundary_utc"`
	PartitionName     string    `json:"partition_name,omitempty"`
	PartitionUpper    time.Time `json:"partition_upper_utc,omitempty"`
	RowsDropped       int64     `json:"rows_dropped_estimate"`
	TerminalGap       bool      `json:"terminal_gap"`
	SourceDroppedAt   time.Time `json:"source_dropped_at,omitempty"`
	Error             string    `json:"error,omitempty"`
}

type BoundaryResult struct {
	Provision ProvisionResult `json:"provision"`
	Expiry    *ExpiryResult   `json:"expiry,omitempty"`
}

type Options struct {
	HoursAhead int
	BlobRoot   string
	DLQRoot    string
}

func DefaultOptions() Options {
	return Options{HoursAhead: HourlyHorizon}
}

func DatabaseUTC(ctx context.Context, db DB) (time.Time, error) {
	var anchor time.Time
	if err := db.QueryRowContext(ctx, `SELECT date_trunc('hour', clock_timestamp())`).Scan(&anchor); err != nil {
		return time.Time{}, fmt.Errorf("lifecycle: read database UTC anchor: %w", err)
	}
	return anchor.UTC(), nil
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
	if err := pgpartition.EnsureHourly(ctx, db, TableQARecords, anchor, opts.HoursAhead); err != nil {
		result.Error = err.Error()
		return result, err
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

// SelectExpiredHour returns the oldest direct hourly child whose catalog upper bound
// is at or before the retention boundary.
func SelectExpiredHour(ctx context.Context, db DB, boundary time.Time) (pgpartition.ChildPartitionBound, bool, error) {
	children, err := pgpartition.ListChildPartitionBounds(ctx, db, TableQARecords)
	if err != nil {
		return pgpartition.ChildPartitionBound{}, false, err
	}
	var candidate *pgpartition.ChildPartitionBound
	for i := range children {
		child := children[i]
		if child.IsDefault {
			continue
		}
		if child.Upper.After(boundary) {
			continue
		}
		if candidate == nil || child.Upper.Before(candidate.Upper) {
			copyChild := child
			candidate = &copyChild
		}
	}
	if candidate == nil {
		return pgpartition.ChildPartitionBound{}, false, nil
	}
	return *candidate, true, nil
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

	shardID, state, restoreOK, err := shardDisposition(ctx, tx, window)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	if !restoreOK || state != archive.StateCommitted {
		shardID, err = control.PersistBoundaryTerminalGap(ctx, tx, window)
		if err != nil {
			result.Error = err.Error()
			return result, fmt.Errorf("lifecycle: terminal gap classification: %w", err)
		}
		result.TerminalGap = true
	}
	qualified := pq.QuoteIdentifier(child.Name)
	if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+qualified); err != nil {
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

func shardDisposition(ctx context.Context, tx *sql.Tx, window archive.Window) (int64, string, bool, error) {
	var shardID int64
	var state sql.NullString
	var restoreVerified sql.NullTime
	err := tx.QueryRowContext(ctx, `
SELECT id, state, restore_verified_at
FROM qa_archive_shards
WHERE window_start = $1 AND generation = 0`, window.Start).Scan(&shardID, &state, &restoreVerified)
	if err == sql.ErrNoRows {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return shardID, state.String, restoreVerified.Valid, nil
}

// RunBoundary executes provision and optional expiry cleanup under separate transactions.
func RunBoundary(ctx context.Context, db *sql.DB, control ControlStore, opts Options) (BoundaryResult, error) {
	var out BoundaryResult
	provision, err := RunProvision(ctx, db, opts, nil)
	out.Provision = provision
	if err != nil {
		return out, err
	}
	anchor := provision.DBAnchor
	boundary := pgpartition.RetentionBoundary(anchor)
	child, ok, err := SelectExpiredHour(ctx, db, boundary)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = conn.Close() }()
	expiry, err := DropExpiredHour(ctx, conn, control, child, boundary)
	out.Expiry = &expiry
	if err != nil {
		return out, err
	}
	if opts.BlobRoot != "" && !child.Lower.IsZero() {
		cleanupErr := cleanupHotFiles(ctx, db, control, child.Lower, opts)
		if cleanupErr != nil {
			return out, cleanupErr
		}
	}
	return out, nil
}

func cleanupHotFiles(ctx context.Context, db *sql.DB, control ControlStore, hourStart time.Time, opts Options) error {
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
	cleanedAt := time.Now().UTC()
	if recordErr := control.RecordHotFilesCleaned(ctx, conn, shardID, cleanedAt, cleanupErr); recordErr != nil {
		return recordErr
	}
	if cleanupErr != "" {
		return fmt.Errorf("lifecycle: hot file cleanup: %s", cleanupErr)
	}
	return nil
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

// ValidateDefaultRemoval rejects DEFAULT removal when it still holds rows.
func ValidateDefaultRemoval(ctx context.Context, db DB) error {
	name, ok, err := pgpartition.DefaultChildPartition(ctx, db, TableQARecords)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	count, err := pgpartition.CountTableRows(ctx, db, name)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("lifecycle: DEFAULT partition %s still holds %d rows", name, count)
	}
	return nil
}

// InventoryRow summarizes one child partition for cutover planning.
type InventoryRow struct {
	Name      string    `json:"name"`
	Lower     time.Time `json:"lower_utc"`
	Upper     time.Time `json:"upper_utc"`
	IsDefault bool      `json:"is_default"`
	RowCount  int64     `json:"row_count"`
	Layout    string    `json:"layout"`
}

// Inventory builds a read-only partition inventory.
func Inventory(ctx context.Context, db DB, anchor time.Time) ([]InventoryRow, error) {
	children, err := pgpartition.ListChildPartitionBounds(ctx, db, TableQARecords)
	if err != nil {
		return nil, err
	}
	out := make([]InventoryRow, 0, len(children))
	for _, child := range children {
		row := InventoryRow{
			Name:      child.Name,
			IsDefault: child.IsDefault,
			Layout:    layoutForChild(child),
		}
		if !child.IsDefault {
			row.Lower = child.Lower
			row.Upper = child.Upper
		}
		count, err := pgpartition.CountTableRows(ctx, db, child.Name)
		if err != nil {
			return nil, err
		}
		row.RowCount = count
		out = append(out, row)
	}
	_ = anchor
	return out, nil
}

func layoutForChild(child pgpartition.ChildPartitionBound) string {
	if child.IsDefault {
		return "default"
	}
	if strings.Contains(child.Name, "rehome") && strings.Contains(child.Name, "staging") {
		return "legacy_staging"
	}
	if child.Upper.Sub(child.Lower) == time.Hour {
		return "hourly"
	}
	if d := child.Upper.Sub(child.Lower); d >= 24*time.Hour && d < 32*24*time.Hour {
		return "monthly"
	}
	return "legacy"
}

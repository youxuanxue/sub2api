package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

// LocalCleanupResult records the bounded 24-hour policy independently of S3.
type LocalCleanupResult struct {
	DBAnchor          time.Time      `json:"db_anchor_utc"`
	RetentionBoundary time.Time      `json:"retention_boundary_utc"`
	ExpiredPartitions int            `json:"expired_partitions"`
	ExpiredFileHours  int            `json:"expired_file_hours"`
	SelectedHours     []time.Time    `json:"selected_hours"`
	Expiries          []ExpiryResult `json:"expiries,omitempty"`
	DryRun            bool           `json:"dry_run"`
}

// RunLocalCleanup implements the 2026-10-21 explicitly approved local retention
// policy. The caller holds both the host lock and QAMA session advisory lock.
// All rows in an hour must be at least 24 hours old before its source is removed.
func RunLocalCleanup(ctx context.Context, conn *sql.Conn, opts Options, dryRun bool) (LocalCleanupResult, error) {
	result := LocalCleanupResult{DryRun: dryRun}
	anchor, err := DatabaseUTC(ctx, conn)
	if err != nil {
		return result, err
	}
	result.DBAnchor = anchor
	result.RetentionBoundary = pgpartition.RetentionBoundary(anchor)
	expired, err := SelectTransitionExpiredHours(ctx, conn, result.RetentionBoundary)
	if err != nil {
		return result, err
	}
	result.ExpiredPartitions = len(expired)
	files, err := expiredHotHours(opts, result.RetentionBoundary)
	if err != nil {
		return result, err
	}
	result.ExpiredFileHours = len(files)
	candidates := map[time.Time]bool{}
	children := map[time.Time]pgpartition.ChildPartitionBound{}
	for _, child := range expired {
		h := child.Lower.UTC()
		candidates[h] = true
		children[h] = child
	}
	for _, h := range files {
		candidates[h] = true
	}
	// Include a committed DROP whose file removal succeeded but completion receipt
	// failed: it may no longer have either a partition or a directory to discover.
	rows, err := conn.QueryContext(ctx, `SELECT window_start FROM qa_archive_shards
 WHERE generation = 0 AND source_dropped_at IS NOT NULL AND hot_files_cleaned_at IS NULL
 AND window_end <= $1 ORDER BY window_start LIMIT $2`, result.RetentionBoundary, MaxPendingHotCleanup)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var h time.Time
		if err = rows.Scan(&h); err != nil {
			_ = rows.Close()
			return result, err
		}
		candidates[h.UTC()] = true
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return result, err
	}
	for h := range candidates {
		result.SelectedHours = append(result.SelectedHours, h)
	}
	sort.Slice(result.SelectedHours, func(i, j int) bool { return result.SelectedHours[i].Before(result.SelectedHours[j]) })
	if len(result.SelectedHours) > MaxTransitionDrops {
		result.SelectedHours = result.SelectedHours[:MaxTransitionDrops]
	}
	if dryRun {
		return result, nil
	}
	control := NewSQLControlAdapter(archive.NewSQLControlStore())
	var joined error
	for _, h := range result.SelectedHours {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(joined, err)
		}
		var expiry ExpiryResult
		if child, ok := children[h]; ok {
			expiry, err = dropFixedAgeHour(ctx, conn, control, child, result.RetentionBoundary, time.Now)
		} else {
			expiry, err = recordAbsentLocalHour(ctx, conn, control, h, result.RetentionBoundary)
		}
		if err == nil {
			var cleaned ExpiryResult
			cleaned, err = ResumeDroppedHourCleanup(ctx, conn, control, h, opts)
			expiry.HotFilesCleaned = cleaned.HotFilesCleaned
		}
		if err != nil {
			expiry.Error = err.Error()
			joined = errors.Join(joined, err)
		}
		result.Expiries = append(result.Expiries, expiry)
	}
	return result, joined
}

// expiredHotHours also finds raw/DLQ files from hours where DB capture failed.
// Only canonical UTC directories are candidates; no mtime-based tree pruning.
func expiredHotHours(opts Options, boundary time.Time) ([]time.Time, error) {
	hours := map[time.Time]bool{}
	for _, item := range []struct{ data, root string }{{opts.BlobRoot, blobRootName}, {opts.DLQRoot, dlqRootName}} {
		if !filepath.IsAbs(item.data) {
			return nil, errors.New("local cleanup requires absolute hot roots")
		}
		base := filepath.Join(item.data, item.root)
		info, err := os.Lstat(base)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("local hot root must be a real directory")
		}
		paths, err := filepath.Glob(filepath.Join(base, "*", "*", "*", "*"))
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return nil, err
			}
			h, err := time.Parse("2006/01/02/15", filepath.ToSlash(rel))
			if err != nil {
				continue
			} // Legacy/non-hour layouts are outside this policy.
			if h.Add(time.Hour).After(boundary) {
				continue
			}
			if err := ValidateHourDir(item.data, item.root, h, path); err != nil {
				return nil, err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return nil, err
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("hour path is not a directory: %s", path)
			}
			hours[h] = true
		}
	}
	out := make([]time.Time, 0, len(hours))
	for h := range hours {
		out = append(out, h)
	}
	return out, nil
}

// A durable source-unavailable receipt makes orphan-hour file cleanup resumable.
// The short parent lock prevents concurrent attachment while absence is checked;
// it is released before filesystem deletion and remains compatible with INSERT.
func recordAbsentLocalHour(ctx context.Context, conn *sql.Conn, control SQLControlAdapter, h, boundary time.Time) (ExpiryResult, error) {
	result := ExpiryResult{RetentionBoundary: boundary, PartitionUpper: h.Add(time.Hour)}
	if !h.Equal(h.Truncate(time.Hour)) || h.Add(time.Hour).After(boundary) {
		return result, errors.New("local orphan hour is not expired")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "LOCK TABLE ONLY public.qa_records IN SHARE UPDATE EXCLUSIVE MODE"); err != nil {
		return result, err
	}
	children, err := pgpartition.ListChildPartitionBounds(ctx, tx, TableQARecords)
	if err != nil {
		return result, err
	}
	for _, child := range children {
		if child.IsDefault || (child.Lower.Before(h.Add(time.Hour)) && child.Upper.After(h)) {
			return result, errors.New("local orphan hour still has a source partition")
		}
	}
	window := archive.Window{Start: h, End: h.Add(time.Hour)}
	id, err := control.PersistBoundaryTerminalGap(ctx, tx, window)
	if err != nil {
		return result, err
	}
	// Keep any original DROP timestamp/name; empty name means source already absent.
	var name sql.NullString
	err = tx.QueryRowContext(ctx, `UPDATE qa_archive_shards SET
 source_dropped_at=COALESCE(source_dropped_at,clock_timestamp()),
 hot_files_cleaned_at=NULL, updated_at=now() WHERE id=$1
 RETURNING source_partition_name, source_dropped_at,
 COALESCE(verification_error_code='source_unavailable_after_retention',false)`, id).Scan(&name, &result.SourceDroppedAt, &result.TerminalGap)
	if err != nil {
		return result, err
	}
	result.PartitionName = name.String
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

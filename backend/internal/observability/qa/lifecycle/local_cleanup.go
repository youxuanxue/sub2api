package lifecycle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

// LocalCleanupResult records the bounded 24-hour policy independently of S3.
type LocalCleanupResult struct {
	DBAnchor            time.Time      `json:"db_anchor_utc"`
	RetentionBoundary   time.Time      `json:"retention_boundary_utc"`
	ExpiredPartitions   int            `json:"expired_partitions"`
	ExpiredFileHours    int            `json:"expired_file_hours"`
	SelectedHours       []time.Time    `json:"selected_hours"`
	Expiries            []ExpiryResult `json:"expiries,omitempty"`
	DryRun              bool           `json:"dry_run"`
	ArchiveReceiptRoot  string         `json:"archive_receipt_root,omitempty"`
	ArchiveEnvironment  string         `json:"archive_environment,omitempty"`
	ReceiptBlockedHours int            `json:"receipt_blocked_hours,omitempty"`
	// The oldest hour still waiting for its node1 receipt. Hours with a
	// verified receipt are reclaimed anyway, so this is the signal that the
	// archive is falling behind (host_https_watch alerts on its age).
	OldestReceiptBlockedHour *time.Time `json:"oldest_receipt_blocked_hour_utc,omitempty"`
	// Why each hour was held back, so an operator can tell "not archived yet"
	// from "a receipt claims an archive that is not on node1".
	ReceiptBlockedReasons []string `json:"receipt_blocked_reasons,omitempty"`
}

type node1Receipt struct {
	SchemaVersion string `json:"schema_version"`
	Environment   string `json:"environment"`
	SourceID      string `json:"source_id"`
	// Set when the hour was streamed to node1 over SSH rather than copied into
	// a locally mounted directory. The payload then lives only on node1, so
	// there is nothing here to re-hash.
	Transport      string    `json:"transport"`
	RemoteDir      string    `json:"remote_dir"`
	WindowStart    time.Time `json:"window_start"`
	WindowEnd      time.Time `json:"window_end"`
	OK             bool      `json:"ok"`
	FileCount      int64     `json:"file_count"`
	TotalBytes     int64     `json:"total_bytes"`
	ManifestSHA256 string    `json:"manifest_sha256"`
}

type node1ManifestEntry struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type node1Manifest struct {
	SchemaVersion string               `json:"schema_version"`
	Environment   string               `json:"environment"`
	WindowStart   time.Time            `json:"window_start"`
	WindowEnd     time.Time            `json:"window_end"`
	FileCount     int64                `json:"file_count"`
	TotalBytes    int64                `json:"total_bytes"`
	Files         []node1ManifestEntry `json:"files"`
}

// hasNode1Receipt is intentionally strict. A JSON file alone is not a receipt:
// it must identify this environment/hour and carry the archive integrity data.
func hasNode1Receipt(root, environment string, h time.Time) (bool, error) {
	if root == "" {
		return true, nil
	}
	if !filepath.IsAbs(root) {
		return false, errors.New("node1 receipt root must be absolute")
	}
	if environment != "prod" && environment != "cn-prod" {
		return false, errors.New("node1 archive environment is required")
	}
	path := filepath.Join(root, environment, h.UTC().Format("2006/01/02/15")+".json")
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("node1 receipt is not a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var receipt node1Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return false, fmt.Errorf("invalid node1 receipt: %w", err)
	}
	start := h.UTC()
	if receipt.SchemaVersion != "qa-node1-hour-v1" || receipt.SourceID != "" || !receipt.OK || receipt.Environment != environment ||
		!receipt.WindowStart.UTC().Equal(start) || !receipt.WindowEnd.UTC().Equal(start.Add(time.Hour)) ||
		receipt.FileCount < 1 || receipt.TotalBytes < 0 || len(receipt.ManifestSHA256) != 64 {
		return false, errors.New("node1 receipt failed integrity checks")
	}
	if receipt.Transport == "ssh-tar" {
		// The payload is on node1, not here: verifyNode1Manifest would re-hash
		// files this host does not have, so it failed for every hour and the
		// gate blocked the entire archive. The push itself is what verifies a
		// streamed hour -- it compares the manifest digest, file count and
		// byte count reported by node1 against the local manifest before
		// writing this receipt, and each archive run re-reads node1's manifest
		// digest before trusting an existing receipt.
		if receipt.RemoteDir == "" {
			return false, errors.New("streamed node1 receipt has no remote_dir")
		}
		return true, nil
	}
	if err := verifyNode1Manifest(filepath.Dir(root), environment, h, receipt); err != nil {
		return false, err
	}
	return true, nil
}

func verifyNode1Manifest(node1Root, environment string, h time.Time, receipt node1Receipt) error {
	target := filepath.Join(node1Root, "qa-raw", environment, h.UTC().Format("2006/01/02/15"))
	manifestPath := filepath.Join(target, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return fmt.Errorf("node1 manifest unavailable: %w", err)
	}
	if manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		return errors.New("node1 manifest is not a regular file")
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read node1 manifest: %w", err)
	}
	digest := sha256.Sum256(manifestBytes)
	if hex.EncodeToString(digest[:]) != receipt.ManifestSHA256 {
		return errors.New("node1 manifest checksum does not match receipt")
	}
	var manifest node1Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("invalid node1 manifest: %w", err)
	}
	start := h.UTC()
	if manifest.SchemaVersion != "qa-node1-hour-v1" || manifest.Environment != environment ||
		!manifest.WindowStart.UTC().Equal(start) || !manifest.WindowEnd.UTC().Equal(start.Add(time.Hour)) ||
		manifest.FileCount != int64(len(manifest.Files)) || manifest.FileCount != receipt.FileCount ||
		manifest.TotalBytes != receipt.TotalBytes || manifest.TotalBytes < 0 {
		return errors.New("node1 manifest identity or aggregate counts are invalid")
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	var total int64
	for _, entry := range manifest.Files {
		if entry.Path == "" || filepath.IsAbs(entry.Path) || filepath.Clean(entry.Path) != entry.Path ||
			entry.Path == "." || entry.Path == ".." || len(entry.SHA256) != 64 || entry.Bytes < 0 {
			return fmt.Errorf("node1 manifest path or digest is invalid: %q", entry.Path)
		}
		if _, ok := seen[entry.Path]; ok {
			return fmt.Errorf("node1 manifest contains duplicate path: %q", entry.Path)
		}
		seen[entry.Path] = struct{}{}
		path := filepath.Join(target, filepath.FromSlash(entry.Path))
		if path != target && !isWithin(target, path) {
			return fmt.Errorf("node1 manifest path escapes target: %q", entry.Path)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("node1 archived file unavailable: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("node1 archived file is not regular: %q", entry.Path)
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open node1 archived file: %w", err)
		}
		hash := sha256.New()
		bytes, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return fmt.Errorf("read node1 archived file: %q", entry.Path)
		}
		if bytes != entry.Bytes || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("node1 archived file checksum mismatch: %q", entry.Path)
		}
		total += bytes
	}
	if total != manifest.TotalBytes {
		return errors.New("node1 manifest total bytes do not match archived files")
	}
	return nil
}

func isWithin(root, path string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// RunLocalCleanup implements the 2026-10-21 explicitly approved local retention
// policy. The caller holds both the host lock and QAMA session advisory lock.
// All rows in an hour must be at least 24 hours old before its source is removed.
func RunLocalCleanup(ctx context.Context, conn *sql.Conn, opts Options, dryRun bool) (LocalCleanupResult, error) {
	result := LocalCleanupResult{DryRun: dryRun}
	result.ArchiveReceiptRoot = opts.ArchiveReceiptRoot
	result.ArchiveEnvironment = opts.ArchiveEnvironment
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
	if opts.ArchiveReceiptRoot != "" {
		gate := gateHoursOnNode1Receipts(opts.ArchiveReceiptRoot, opts.ArchiveEnvironment, result.SelectedHours)
		result.SelectedHours = gate.Allowed
		result.ReceiptBlockedHours = gate.Blocked
		result.OldestReceiptBlockedHour = gate.OldestBlocked
		receiptErr := gate.Err
		if result.ReceiptBlockedHours > 0 {
			// Holding an hour back IS the gate working: the copy it would have
			// deleted is not provably on node1. Report it as a result, not an
			// error -- returning an error made the systemd unit exit non-zero,
			// which the host job watcher alerted on as a service failure. The
			// real failures (database unreachable, partition detach refused)
			// still propagate below.
			if receiptErr != nil {
				result.ReceiptBlockedReasons = append(result.ReceiptBlockedReasons,
					receiptErr.Error())
			} else {
				result.ReceiptBlockedReasons = append(result.ReceiptBlockedReasons,
					"node1 archive receipt missing")
			}
		}
	}
	// Bound drops per run after the receipt gate, not before: truncating first
	// let a run of unarchived oldest hours fill every slot, so newer hours that
	// node1 already holds were never reclaimed.
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

// receiptGate is the per-hour node1 receipt decision for one cleanup batch.
type receiptGate struct {
	Allowed       []time.Time
	Blocked       int
	OldestBlocked *time.Time
	Err           error
}

// gateHoursOnNode1Receipts gates each hour on its own receipt: an hour is
// dropped only when node1 provably holds it. Holding the whole batch back
// because one hour was missing let two failed pushes block 62 verified hours
// on prod (2026-11-08) while the disk kept filling. The archive falling behind
// is reported through OldestBlocked instead. hours must be sorted ascending.
func gateHoursOnNode1Receipts(root, environment string, hours []time.Time) receiptGate {
	gate := receiptGate{Allowed: make([]time.Time, 0, len(hours))}
	for _, h := range hours {
		ok, checkErr := hasNode1Receipt(root, environment, h)
		if checkErr != nil {
			gate.Err = errors.Join(gate.Err, fmt.Errorf("%s: %w", h.Format(time.RFC3339), checkErr))
		}
		if ok {
			gate.Allowed = append(gate.Allowed, h)
			continue
		}
		gate.Blocked++
		if gate.OldestBlocked == nil {
			blocked := h
			gate.OldestBlocked = &blocked
		}
	}
	return gate
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

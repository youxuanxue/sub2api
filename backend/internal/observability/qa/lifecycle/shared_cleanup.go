package lifecycle

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

type SourceCompletion struct {
	State          string `json:"state"`
	ManifestSHA256 string `json:"manifest_sha256"`
	SealDigest     string `json:"seal_digest"`
}

// SharedCleanupPlan is a coordinator decision, not a legacy archive receipt.
// This path never writes hot_files_cleaned_at: application disks are independent.
type SharedCleanupPlan struct {
	SchemaVersion   string                      `json:"schema_version"`
	Environment     string                      `json:"environment"`
	WindowStart     time.Time                   `json:"window_start"`
	WindowEnd       time.Time                   `json:"window_end"`
	ExpectedSources []string                    `json:"expected_sources"`
	Sources         map[string]SourceCompletion `json:"sources"`
	Action          string                      `json:"shared_partition_action"`
}

func (p SharedCleanupPlan) Validate(environment string, expected []string, anchor time.Time) error {
	if (environment != "prod" && environment != "cn-prod") || p.Environment != environment ||
		p.SchemaVersion != "qa-data-machine-cleanup-v1" || p.Action != "delete" ||
		p.WindowStart.IsZero() || !p.WindowStart.Equal(p.WindowStart.UTC().Truncate(time.Hour)) ||
		!p.WindowEnd.Equal(p.WindowStart.Add(time.Hour)) ||
		p.WindowEnd.After(pgpartition.RetentionBoundary(anchor)) {
		return errors.New("shared QA cleanup plan identity, action or retention boundary invalid")
	}
	if len(expected) == 0 || len(p.ExpectedSources) != len(expected) || len(p.Sources) != len(expected) {
		return errors.New("shared QA cleanup requires the exact historical source set")
	}
	a, b := append([]string(nil), expected...), append([]string(nil), p.ExpectedSources...)
	sort.Strings(a)
	sort.Strings(b)
	for i, source := range a {
		if source == "" || source != b[i] || (i > 0 && source == a[i-1]) {
			return errors.New("shared QA cleanup source set mismatch")
		}
		completion, ok := p.Sources[source]
		if !ok || completion.State != "complete" {
			return errors.New("shared QA cleanup source is incomplete")
		}
		for _, digest := range []string{completion.ManifestSHA256, completion.SealDigest} {
			if value, err := hex.DecodeString(digest); err != nil || len(value) != 32 {
				return errors.New("shared QA cleanup source evidence digest invalid")
			}
		}
	}
	return nil
}

func RunSharedCleanup(ctx context.Context, conn *sql.Conn, plan SharedCleanupPlan, environment string, expected []string, dryRun bool) (ExpiryResult, error) {
	anchor, err := DatabaseUTC(ctx, conn)
	if err != nil {
		return ExpiryResult{}, err
	}
	if err := plan.Validate(environment, expected, anchor); err != nil {
		return ExpiryResult{}, err
	}
	boundary := pgpartition.RetentionBoundary(anchor)
	children, err := SelectTransitionExpiredHours(ctx, conn, boundary)
	if err != nil {
		return ExpiryResult{}, err
	}
	for _, child := range children {
		if !child.Lower.Equal(plan.WindowStart) {
			continue
		}
		if dryRun {
			return ExpiryResult{DBAnchor: anchor, PartitionName: child.Name, PartitionUpper: child.Upper, RetentionBoundary: boundary}, nil
		}
		control := NewSQLControlAdapter(archive.NewSQLControlStore())
		return dropFixedAgeHour(ctx, conn, control, child, boundary, time.Now)
	}
	// A repeated DROP is successful only when a durable control record proves it.
	var dropped time.Time
	err = conn.QueryRowContext(ctx, `SELECT source_dropped_at FROM qa_archive_shards
 WHERE window_start=$1 AND generation=0 AND source_dropped_at IS NOT NULL`, plan.WindowStart).Scan(&dropped)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if dryRun {
				return ExpiryResult{DBAnchor: anchor, RetentionBoundary: boundary, PartitionUpper: plan.WindowEnd}, nil
			}
			control := NewSQLControlAdapter(archive.NewSQLControlStore())
			return recordAbsentLocalHour(ctx, conn, control, plan.WindowStart, boundary)
		}
		return ExpiryResult{}, fmt.Errorf("shared QA hour absent without durable DROP evidence: %w", err)
	}
	return ExpiryResult{DBAnchor: anchor, SourceDroppedAt: dropped, RetentionBoundary: boundary, PartitionUpper: plan.WindowEnd}, nil
}

// RunSharedRetention is the explicitly approved loss-tolerant expiry path.
// It never inspects application disks or waits for archive/source receipts.
func RunSharedRetention(ctx context.Context, conn *sql.Conn, dryRun bool) (LocalCleanupResult, error) {
	result := LocalCleanupResult{DryRun: dryRun}
	anchor, err := DatabaseUTC(ctx, conn)
	if err != nil {
		return result, err
	}
	result.DBAnchor = anchor
	result.RetentionBoundary = pgpartition.RetentionBoundary(anchor)
	children, err := SelectTransitionExpiredHours(ctx, conn, result.RetentionBoundary)
	if err != nil {
		return result, err
	}
	result.ExpiredPartitions = len(children)
	control := NewSQLControlAdapter(archive.NewSQLControlStore())
	var joined error
	for i, child := range children {
		if i >= MaxTransitionDrops {
			break
		}
		if err := ctx.Err(); err != nil {
			return result, errors.Join(joined, err)
		}
		result.SelectedHours = append(result.SelectedHours, child.Lower.UTC())
		if dryRun {
			continue
		}
		expiry, dropErr := dropFixedAgeHour(ctx, conn, control, child, result.RetentionBoundary, time.Now)
		if dropErr != nil {
			expiry.Error = dropErr.Error()
			joined = errors.Join(joined, dropErr)
		}
		result.Expiries = append(result.Expiries, expiry)
	}
	return result, joined
}

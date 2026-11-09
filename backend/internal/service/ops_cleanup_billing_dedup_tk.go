package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// UsageBillingDedupArchiveCleaner bounds the dedup archive, which upstream keeps
// forever. It is an optional extension of DashboardAggregationRepository so the
// upstream interface and its stubs stay unchanged.
type UsageBillingDedupArchiveCleaner interface {
	// PurgeExpiredUsageBillingDedup deletes hot-table keys older than cutoff
	// without archiving them. more reports that rows may remain past the per-run cap.
	PurgeExpiredUsageBillingDedup(ctx context.Context, cutoff time.Time) (deleted int64, more bool, err error)
	// CleanupUsageBillingDedupArchive deletes archived keys older than cutoff.
	CleanupUsageBillingDedupArchive(ctx context.Context, cutoff time.Time) (deleted int64, more bool, err error)
}

// cleanupUsageBillingDedup keeps billingDedupRetentionDays of keys in the hot
// table and billingDedupArchiveRetentionDays in hot plus archive together.
//
// Order matters while a backlog drains. Keys already past the archive window are
// deleted from the hot table first, and the upstream hot-to-archive move is
// skipped while that purge still has rows left, so expired keys are never copied
// into the archive only to be deleted again.
func (s *OpsCleanupService) cleanupUsageBillingDedup(ctx context.Context, now time.Time, policy dataLifecyclePolicy) error {
	archiveCutoff := now.AddDate(0, 0, -policy.billingDedupArchiveRetentionDays)
	hotCutoff := now.AddDate(0, 0, -policy.billingDedupRetentionDays)

	cleaner, ok := s.dashboardRepo.(UsageBillingDedupArchiveCleaner)
	if !ok {
		return s.dashboardRepo.CleanupUsageBillingDedup(ctx, hotCutoff)
	}

	purged, backlog, err := cleaner.PurgeExpiredUsageBillingDedup(ctx, archiveCutoff)
	if err != nil {
		return fmt.Errorf("purge expired keys: %w", err)
	}
	if !backlog {
		if err := s.dashboardRepo.CleanupUsageBillingDedup(ctx, hotCutoff); err != nil {
			return err
		}
	}
	archivePurged, archiveBacklog, err := cleaner.CleanupUsageBillingDedupArchive(ctx, archiveCutoff)
	if err != nil {
		return fmt.Errorf("cleanup archive: %w", err)
	}

	logger.L().Info("[OpsCleanup] usage_billing_dedup lifecycle",
		zap.String("component", "service.ops_cleanup"),
		zap.Int("hot_retention_days", policy.billingDedupRetentionDays),
		zap.Int("archive_retention_days", policy.billingDedupArchiveRetentionDays),
		zap.Int64("hot_purged", purged),
		zap.Bool("hot_backlog", backlog),
		zap.Bool("archive_move_skipped", backlog),
		zap.Int64("archive_purged", archivePurged),
		zap.Bool("archive_backlog", archiveBacklog),
	)
	return nil
}

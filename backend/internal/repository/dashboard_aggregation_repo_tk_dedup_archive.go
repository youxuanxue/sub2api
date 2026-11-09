package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Upstream keeps usage_billing_dedup_archive forever because upstream dedup keys
// can come from client-supplied request IDs. TokenKey keys every request with a
// gateway-generated UUID, so an expired key is never claimed again; it only has
// to outlive the longest-lived money event it guards. Both methods cap one run at
// the same row count as the upstream hot-table cleanup, so a large backlog drains
// over several nightly runs instead of one long transaction.
const (
	usageBillingDedupPurgeBatchSize     = 10000
	usageBillingDedupPurgeMaxRowsPerRun = 1_000_000
)

var _ service.UsageBillingDedupArchiveCleaner = (*dashboardAggregationRepository)(nil)

// PurgeExpiredUsageBillingDedup deletes hot-table keys created before cutoff
// without archiving them. more reports that the per-run cap was reached, so rows
// older than cutoff may remain.
func (r *dashboardAggregationRepository) PurgeExpiredUsageBillingDedup(ctx context.Context, cutoff time.Time) (int64, bool, error) {
	// ctid batches ride the created_at BRIN index, as the upstream archive step does.
	return r.purgeUsageBillingDedupBatches(ctx, `
		WITH victims AS (
			SELECT ctid
			FROM usage_billing_dedup
			WHERE created_at < $1
			LIMIT $2
		)
		DELETE FROM usage_billing_dedup
		WHERE ctid IN (SELECT ctid FROM victims)
	`, cutoff)
}

// CleanupUsageBillingDedupArchive deletes archived keys created before cutoff.
// The archive has no created_at index, so batches are unordered like the hot-table
// steps; a sort here would read the whole archive on every batch.
func (r *dashboardAggregationRepository) CleanupUsageBillingDedupArchive(ctx context.Context, cutoff time.Time) (int64, bool, error) {
	return r.purgeUsageBillingDedupBatches(ctx, `
		WITH victims AS (
			SELECT request_id, api_key_id
			FROM usage_billing_dedup_archive
			WHERE created_at < $1
			LIMIT $2
		)
		DELETE FROM usage_billing_dedup_archive a
		USING victims v
		WHERE a.request_id = v.request_id AND a.api_key_id = v.api_key_id
	`, cutoff)
}

func (r *dashboardAggregationRepository) purgeUsageBillingDedupBatches(ctx context.Context, query string, cutoff time.Time) (int64, bool, error) {
	var total int64
	for {
		limit := min(int64(usageBillingDedupPurgeBatchSize), usageBillingDedupPurgeMaxRowsPerRun-total)
		res, err := r.sql.ExecContext(ctx, query, cutoff.UTC(), limit)
		if err != nil {
			return total, false, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return total, false, err
		}
		total += affected
		if affected < limit {
			return total, false, nil
		}
		if total >= usageBillingDedupPurgeMaxRowsPerRun {
			return total, true, nil
		}
	}
}

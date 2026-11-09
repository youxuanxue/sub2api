//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The three lifecycle steps against real Postgres: keys past the archive window
// are deleted from the hot table without being archived, keys inside it move to
// the archive, recent keys stay hot, and expired archive rows are deleted.
func TestUsageBillingDedupLifecycle_PurgesExpiredAndKeepsWindow(t *testing.T) {
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(integrationDB)
	now := time.Now().UTC()
	archiveCutoff := now.AddDate(0, 0, -30)
	hotCutoff := now.AddDate(0, 0, -7)

	expiredHot := "tk-dedup-expired-hot-" + uuid.NewString()
	agingHot := "tk-dedup-aging-hot-" + uuid.NewString()
	freshHot := "tk-dedup-fresh-hot-" + uuid.NewString()
	expiredArchive := "tk-dedup-expired-archive-" + uuid.NewString()
	keptArchive := "tk-dedup-kept-archive-" + uuid.NewString()

	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint, created_at)
		VALUES ($1, 1, $4, $5), ($2, 1, $4, $6), ($3, 1, $4, $7)
	`, expiredHot, agingHot, freshHot, strings.Repeat("a", 64),
		now.AddDate(0, 0, -45), now.AddDate(0, 0, -10), now.Add(-time.Hour))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO usage_billing_dedup_archive (request_id, api_key_id, request_fingerprint, created_at)
		VALUES ($1, 1, $3, $4), ($2, 1, $3, $5)
	`, expiredArchive, keptArchive, strings.Repeat("b", 64),
		now.AddDate(0, 0, -60), now.AddDate(0, 0, -20))
	require.NoError(t, err)

	purged, more, err := repo.PurgeExpiredUsageBillingDedup(ctx, archiveCutoff)
	require.NoError(t, err)
	require.False(t, more)
	require.GreaterOrEqual(t, purged, int64(1))
	require.NoError(t, repo.CleanupUsageBillingDedup(ctx, hotCutoff))
	_, more, err = repo.CleanupUsageBillingDedupArchive(ctx, archiveCutoff)
	require.NoError(t, err)
	require.False(t, more)

	count := func(table, requestID string) int {
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE request_id = $1", requestID).Scan(&n))
		return n
	}
	require.Zero(t, count("usage_billing_dedup", expiredHot))
	require.Zero(t, count("usage_billing_dedup_archive", expiredHot), "expired hot keys are deleted, not archived")
	require.Zero(t, count("usage_billing_dedup", agingHot))
	require.Equal(t, 1, count("usage_billing_dedup_archive", agingHot), "keys inside the archive window still move to the archive")
	require.Equal(t, 1, count("usage_billing_dedup", freshHot))
	require.Zero(t, count("usage_billing_dedup_archive", expiredArchive))
	require.Equal(t, 1, count("usage_billing_dedup_archive", keptArchive))
}

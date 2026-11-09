package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// dedupArchiveCleanerStub adds the TK archive extension to the shared dashboard stub
// and records the order in which the three lifecycle steps run.
type dedupArchiveCleanerStub struct {
	*dashboardAggregationRepoTestStub
	calls          []string
	purgeCutoff    time.Time
	archiveCutoff  time.Time
	purgeBacklog   bool
	purgeErr       error
	archiveErr     error
	purgeDeleted   int64
	archiveDeleted int64
}

func (s *dedupArchiveCleanerStub) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	s.calls = append(s.calls, "archive_move")
	return s.dashboardAggregationRepoTestStub.CleanupUsageBillingDedup(ctx, cutoff)
}

func (s *dedupArchiveCleanerStub) PurgeExpiredUsageBillingDedup(_ context.Context, cutoff time.Time) (int64, bool, error) {
	s.calls = append(s.calls, "hot_purge")
	s.purgeCutoff = cutoff
	return s.purgeDeleted, s.purgeBacklog, s.purgeErr
}

func (s *dedupArchiveCleanerStub) CleanupUsageBillingDedupArchive(_ context.Context, cutoff time.Time) (int64, bool, error) {
	s.calls = append(s.calls, "archive_purge")
	s.archiveCutoff = cutoff
	return s.archiveDeleted, false, s.archiveErr
}

func newDedupCleanupService(repo DashboardAggregationRepository) *OpsCleanupService {
	return &OpsCleanupService{dashboardRepo: repo, cfg: &config.Config{}}
}

func TestCleanupUsageBillingDedup_SteadyStateRunsAllStepsInOrder(t *testing.T) {
	stub := &dedupArchiveCleanerStub{dashboardAggregationRepoTestStub: &dashboardAggregationRepoTestStub{}}
	svc := newDedupCleanupService(stub)
	now := time.Now().UTC()
	policy := resolveDataLifecyclePolicy(nil, config.OpsCleanupConfig{})

	require.NoError(t, svc.cleanupUsageBillingDedup(context.Background(), now, policy))

	// Expired keys leave the hot table before the upstream move runs, so the
	// move never copies a key into the archive only to delete it again.
	require.Equal(t, []string{"hot_purge", "archive_move", "archive_purge"}, stub.calls)
	require.Equal(t, now.AddDate(0, 0, -30), stub.purgeCutoff)
	require.Equal(t, now.AddDate(0, 0, -30), stub.archiveCutoff)
	require.Equal(t, now.AddDate(0, 0, -7), stub.lastDedupCutoff)
}

func TestCleanupUsageBillingDedup_BacklogSkipsArchiveMove(t *testing.T) {
	// The first nights after the change: millions of keys are older than 30 days.
	// Moving them would bloat the archive with rows that are already expired.
	stub := &dedupArchiveCleanerStub{
		dashboardAggregationRepoTestStub: &dashboardAggregationRepoTestStub{},
		purgeBacklog:                     true,
		purgeDeleted:                     1_000_000,
	}
	svc := newDedupCleanupService(stub)

	require.NoError(t, svc.cleanupUsageBillingDedup(context.Background(), time.Now().UTC(), resolveDataLifecyclePolicy(nil, config.OpsCleanupConfig{})))

	require.Equal(t, []string{"hot_purge", "archive_purge"}, stub.calls)
	require.Zero(t, stub.cleanupDedupCalls)
}

func TestCleanupUsageBillingDedup_PurgeErrorStopsBeforeMove(t *testing.T) {
	stub := &dedupArchiveCleanerStub{
		dashboardAggregationRepoTestStub: &dashboardAggregationRepoTestStub{},
		purgeErr:                         errors.New("statement timeout"),
	}
	svc := newDedupCleanupService(stub)

	err := svc.cleanupUsageBillingDedup(context.Background(), time.Now().UTC(), resolveDataLifecyclePolicy(nil, config.OpsCleanupConfig{}))

	require.ErrorContains(t, err, "purge expired keys")
	require.Equal(t, []string{"hot_purge"}, stub.calls)
}

func TestCleanupUsageBillingDedup_ArchiveErrorIsReported(t *testing.T) {
	stub := &dedupArchiveCleanerStub{
		dashboardAggregationRepoTestStub: &dashboardAggregationRepoTestStub{},
		archiveErr:                       errors.New("lock timeout"),
	}
	svc := newDedupCleanupService(stub)

	err := svc.cleanupUsageBillingDedup(context.Background(), time.Now().UTC(), resolveDataLifecyclePolicy(nil, config.OpsCleanupConfig{}))

	require.ErrorContains(t, err, "cleanup archive")
}

func TestCleanupUsageBillingDedup_RepositoryWithoutExtensionKeepsUpstreamMove(t *testing.T) {
	// Stubs and non-TK repositories only implement the upstream interface: they
	// still get the hot-table move, and the archive is left alone.
	stub := &dashboardAggregationRepoTestStub{}
	svc := newDedupCleanupService(stub)
	now := time.Now().UTC()

	require.NoError(t, svc.cleanupUsageBillingDedup(context.Background(), now, resolveDataLifecyclePolicy(nil, config.OpsCleanupConfig{})))

	require.Equal(t, 1, stub.cleanupDedupCalls)
	require.Equal(t, now.AddDate(0, 0, -7), stub.lastDedupCutoff)
}

func TestBillingDedupArchiveOutlivesSlowestSettlement(t *testing.T) {
	// A batch-image release looks up its hold key in hot+archive before refunding
	// frozen balance. If the key were gone, the release is skipped and the user's
	// frozen balance never returns. The archive window must outlive the in-flight
	// TTL, with room for a delayed release.
	// batch_image.inflight_ttl_seconds default (config.go) and the queue fallback
	// (repository/batch_image_queue.go) are both 7 days.
	inflight := 7 * 24 * time.Hour
	archive := time.Duration(billingDedupArchiveRetentionDays) * 24 * time.Hour
	require.GreaterOrEqual(t, archive, 2*inflight)
	require.GreaterOrEqual(t, archive, 2*grokVideoBilledClaimTTL(nil))
	require.Greater(t, billingDedupArchiveRetentionDays, billingDedupRetentionDays)
}

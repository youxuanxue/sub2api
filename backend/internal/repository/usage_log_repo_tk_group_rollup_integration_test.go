//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func indexByGroup(rows []usagestats.GroupUsageSummary) map[int64]usagestats.GroupUsageSummary {
	out := make(map[int64]usagestats.GroupUsageSummary, len(rows))
	for _, r := range rows {
		out[r.GroupID] = r
	}
	return out
}

func indexGroupStats(rows []usagestats.GroupStat) map[int64]usagestats.GroupStat {
	out := make(map[int64]usagestats.GroupStat, len(rows))
	for _, r := range rows {
		out[r.GroupID] = r
	}
	return out
}

func (s *UsageLogRepoSuite) rollupParityCreateUngroupedLog(user *service.User, apiKey *service.APIKey, account *service.Account, in, out, cacheCreate, cacheRead int, cost float64, createdAt time.Time) {
	log := &service.UsageLog{
		UserID:              user.ID,
		APIKeyID:            apiKey.ID,
		AccountID:           account.ID,
		Model:               "claude-3",
		InputTokens:         in,
		OutputTokens:        out,
		CacheCreationTokens: cacheCreate,
		CacheReadTokens:     cacheRead,
		TotalCost:           cost,
		ActualCost:          cost,
		CreatedAt:           createdAt,
	}
	_, err := s.repo.Create(s.ctx, log)
	s.Require().NoError(err)
}

// TestGroupRollupParity_EqualsLegacyRawScan is the load-bearing equality test for
// the per-(group, day) rollup that backs GetAllGroupUsageSummary. It builds a
// fixture spanning two groups over completed past days + today, then asserts:
//
//  1. BEFORE aggregation (no backfill marker) the legacy raw full-table scan path
//     serves correct totals — this is the self-healing fallback.
//  2. AFTER AggregateRange (which runs the one-time backfill + sets the marker)
//     the rollup-backed path returns byte-identical totals (completed days from
//     the rollup + today's partial day from raw), proving the structural rewrite
//     did not change the numbers.
func (s *UsageLogRepoSuite) TestGroupRollupParity_EqualsLegacyRawScan() {
	now := time.Now()
	today := timezone.Today()
	day5 := today.Add(-5 * 24 * time.Hour).Add(9 * time.Hour)
	day2 := today.Add(-2 * 24 * time.Hour).Add(14 * time.Hour)
	todayPoint := today.Add(3 * time.Hour)
	if todayPoint.After(now) {
		todayPoint = now.Add(-time.Minute)
	}

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "grp-rollup@test.com"})
	key := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-grp-rollup", Name: "k"})
	acc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "grp-rollup-acc", Platform: domain.PlatformAnthropic})
	grpA := mustCreateGroup(s.T(), s.client, &service.Group{Name: "grp-rollup-A", Platform: domain.PlatformAnthropic})
	grpB := mustCreateGroup(s.T(), s.client, &service.Group{Name: "grp-rollup-B", Platform: domain.PlatformOpenAI})

	// group A: day5 0.50, day2 0.30, today 0.10 -> total 0.90, today 0.10
	s.rollupParityCreateLog(user, key, acc, grpA.ID, 10, 20, 0, 0, 0.50, day5)
	s.rollupParityCreateLog(user, key, acc, grpA.ID, 10, 20, 0, 0, 0.30, day2)
	s.rollupParityCreateLog(user, key, acc, grpA.ID, 10, 20, 0, 0, 0.10, todayPoint)
	// group B: day2 0.70, today 0.20 -> total 0.90, today 0.20
	s.rollupParityCreateLog(user, key, acc, grpB.ID, 10, 20, 0, 0, 0.70, day2)
	s.rollupParityCreateLog(user, key, acc, grpB.ID, 10, 20, 0, 0, 0.20, todayPoint)

	// (1) Fallback path: before aggregation the marker is absent, so the legacy
	// raw scan serves it.
	pre, err := s.repo.GetAllGroupUsageSummary(s.ctx, today)
	s.Require().NoError(err)
	preIdx := indexByGroup(pre)
	s.InDelta(0.90, preIdx[grpA.ID].TotalCost, 1e-9, "fallback raw scan: group A total")
	s.InDelta(0.10, preIdx[grpA.ID].TodayCost, 1e-9)
	s.InDelta(0.90, preIdx[grpB.ID].TotalCost, 1e-9)
	s.InDelta(0.20, preIdx[grpB.ID].TodayCost, 1e-9)

	// Populate the rollup + set the backfill marker via the aggregation driver.
	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	s.Require().NoError(aggRepo.AggregateRange(s.ctx, today.Add(-30*24*time.Hour), today), "AggregateRange")

	// (2) Rollup path: same totals, now served from rollup(completed days)+raw(today).
	post, err := s.repo.GetAllGroupUsageSummary(s.ctx, today)
	s.Require().NoError(err)
	postIdx := indexByGroup(post)
	s.InDelta(0.90, postIdx[grpA.ID].TotalCost, 1e-9, "rollup path: group A total from rollup(day5+day2)+raw(today)")
	s.InDelta(0.10, postIdx[grpA.ID].TodayCost, 1e-9)
	s.InDelta(0.90, postIdx[grpB.ID].TotalCost, 1e-9)
	s.InDelta(0.20, postIdx[grpB.ID].TodayCost, 1e-9)

	// Parity: rollup path equals the legacy raw path element-for-element.
	s.InDelta(preIdx[grpA.ID].TotalCost, postIdx[grpA.ID].TotalCost, 1e-9)
	s.InDelta(preIdx[grpA.ID].TodayCost, postIdx[grpA.ID].TodayCost, 1e-9)
	s.InDelta(preIdx[grpB.ID].TotalCost, postIdx[grpB.ID].TotalCost, 1e-9)
	s.InDelta(preIdx[grpB.ID].TodayCost, postIdx[grpB.ID].TodayCost, 1e-9)
}

// TestGroupStatsRollupParity_EqualsLegacyRawScanWithUngrouped is the equality
// test for Dashboard/Usage group distribution. It locks the legacy
// COALESCE(group_id, 0) bucket for ungrouped usage while proving completed days
// served from usage_dashboard_group_daily plus raw today equals the legacy raw
// query.
func (s *UsageLogRepoSuite) TestGroupStatsRollupParity_EqualsLegacyRawScanWithUngrouped() {
	now := time.Now()
	today := timezone.Today()
	start := today.Add(-6 * 24 * time.Hour)
	day5 := today.Add(-5 * 24 * time.Hour).Add(9 * time.Hour)
	day2 := today.Add(-2 * 24 * time.Hour).Add(14 * time.Hour)
	todayPoint := today.Add(3 * time.Hour)
	if todayPoint.After(now) {
		todayPoint = now.Add(-time.Minute)
	}
	end := todayPoint.Add(time.Minute)

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "grp-stats-rollup@test.com"})
	key := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-grp-stats-rollup", Name: "k"})
	acc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "grp-stats-rollup-acc", Platform: domain.PlatformAnthropic})
	grpA := mustCreateGroup(s.T(), s.client, &service.Group{Name: "grp-stats-rollup-A", Platform: domain.PlatformAnthropic})
	grpB := mustCreateGroup(s.T(), s.client, &service.Group{Name: "grp-stats-rollup-B", Platform: domain.PlatformOpenAI})

	// Real groups across completed days + today's raw tail.
	s.rollupParityCreateLog(user, key, acc, grpA.ID, 10, 20, 0, 0, 0.50, day5)
	s.rollupParityCreateLog(user, key, acc, grpA.ID, 5, 7, 1, 2, 0.10, todayPoint)
	s.rollupParityCreateLog(user, key, acc, grpB.ID, 2, 3, 0, 0, 0.20, day2)
	// Ungrouped usage must stay visible as group_id=0 in group distribution.
	s.rollupParityCreateUngroupedLog(user, key, acc, 3, 4, 5, 6, 0.20, day2)
	s.rollupParityCreateUngroupedLog(user, key, acc, 7, 8, 0, 1, 0.05, todayPoint)

	// Before aggregation the metrics marker is absent, so this is the legacy raw
	// path. It is the ground-truth reference for the rollup path below.
	pre, err := s.repo.GetGroupStatsWithFilters(s.ctx, start, end, 0, 0, 0, 0, nil, nil, nil)
	s.Require().NoError(err)
	preIdx := indexGroupStats(pre)
	s.Require().Contains(preIdx, int64(0), "legacy raw path must expose ungrouped usage as group_id=0")
	s.Equal(int64(2), preIdx[0].Requests)
	s.Equal(int64(34), preIdx[0].TotalTokens)
	s.InDelta(0.25, preIdx[0].ActualCost, 1e-9)

	// Populate the group daily rollup and set both backfill markers. The read path
	// now uses completed days from usage_dashboard_group_daily plus raw today.
	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	s.Require().NoError(aggRepo.AggregateRange(s.ctx, start, today), "AggregateRange")

	post, err := s.repo.GetGroupStatsWithFilters(s.ctx, start, end, 0, 0, 0, 0, nil, nil, nil)
	s.Require().NoError(err)
	postIdx := indexGroupStats(post)
	s.Require().Len(postIdx, len(preIdx))
	for groupID, want := range preIdx {
		got, ok := postIdx[groupID]
		s.Require().True(ok, "rollup path missing group_id=%d", groupID)
		s.Equal(want.GroupName, got.GroupName, "group_name group_id=%d", groupID)
		s.Equal(want.Requests, got.Requests, "requests group_id=%d", groupID)
		s.Equal(want.TotalTokens, got.TotalTokens, "tokens group_id=%d", groupID)
		s.InDelta(want.Cost, got.Cost, 1e-9, "cost group_id=%d", groupID)
		s.InDelta(want.ActualCost, got.ActualCost, 1e-9, "actual_cost group_id=%d", groupID)
		s.InDelta(want.AccountCost, got.AccountCost, 1e-9, "account_cost group_id=%d", groupID)
	}

	filtered, err := s.repo.GetGroupStatsWithFilters(s.ctx, start, end, 0, 0, 0, grpA.ID, nil, nil, nil)
	s.Require().NoError(err)
	s.Require().Len(filtered, 1)
	s.Equal(grpA.ID, filtered[0].GroupID)
	s.Equal(preIdx[grpA.ID].Requests, filtered[0].Requests)
	s.Equal(preIdx[grpA.ID].TotalTokens, filtered[0].TotalTokens)
	s.InDelta(preIdx[grpA.ID].ActualCost, filtered[0].ActualCost, 1e-9)
}

// TestGroupStatsRollupWaitsForMetricsBackfill covers the deploy path for
// databases that already ran tk_038 before tk_046 added the metric columns. The
// old all-time cost marker may exist while historical metric columns are still
// zero, so the group-distribution read path must ignore the rollup until the
// metrics marker is present and backfillGroupDailyMetricsAllOnce has rewritten
// historical rows.
func (s *UsageLogRepoSuite) TestGroupStatsRollupWaitsForMetricsBackfill() {
	today := timezone.Today()
	start := today.Add(-6 * 24 * time.Hour)
	day5 := today.Add(-5 * 24 * time.Hour).Add(9 * time.Hour)
	end := today

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "grp-stats-metrics-backfill@test.com"})
	key := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-grp-stats-metrics-backfill", Name: "k"})
	acc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "grp-stats-metrics-backfill-acc", Platform: domain.PlatformAnthropic})
	grp := mustCreateGroup(s.T(), s.client, &service.Group{Name: "grp-stats-metrics-backfill", Platform: domain.PlatformAnthropic})
	s.rollupParityCreateLog(user, key, acc, grp.ID, 11, 13, 2, 3, 0.42, day5)

	// Simulate an old deployment: tk_038's historical cost backfill marker exists
	// and there is a completed-day row with actual_cost, but tk_046's metric
	// marker is missing and the new metric columns still have their zero defaults.
	_, err := s.tx.ExecContext(s.ctx, `
		INSERT INTO usage_dashboard_group_daily (bucket_date, group_id, actual_cost, computed_at)
		VALUES
			($1::date, $2, 0.42, NOW()),
			(DATE '`+groupDailyBackfillMarkerDate+`', 0, 0, NOW())
		ON CONFLICT (bucket_date, group_id) DO UPDATE SET
			actual_cost = EXCLUDED.actual_cost,
			computed_at = EXCLUDED.computed_at
	`, day5.Format("2006-01-02"), grp.ID)
	s.Require().NoError(err)

	metricsReady, err := s.repo.groupDailyMetricsBackfilled(s.ctx)
	s.Require().NoError(err)
	s.False(metricsReady, "metric marker is absent, so production fast path must not trust zero-filled metric columns")

	rawBefore, err := s.repo.GetGroupStatsWithFilters(s.ctx, start, end, 0, 0, 0, 0, nil, nil, nil)
	s.Require().NoError(err)
	rawIdx := indexGroupStats(rawBefore)
	s.Equal(int64(29), rawIdx[grp.ID].TotalTokens)
	s.InDelta(0.42, rawIdx[grp.ID].ActualCost, 1e-9)

	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	s.Require().NoError(aggRepo.backfillGroupDailyMetricsAllOnce(s.ctx))

	metricsReady, err = s.repo.groupDailyMetricsBackfilled(s.ctx)
	s.Require().NoError(err)
	s.True(metricsReady, "metric marker should enable the production group stats rollup after rewrite")

	var row struct {
		Requests    int64
		InputTokens int64
		OutputToken int64
		CacheCreate int64
		CacheRead   int64
		ActualCost  float64
	}
	err = scanSingleRow(s.ctx, s.tx, `
		SELECT total_requests, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, actual_cost
		FROM usage_dashboard_group_daily
		WHERE bucket_date = $1::date AND group_id = $2
	`, []any{day5.Format("2006-01-02"), grp.ID},
		&row.Requests,
		&row.InputTokens,
		&row.OutputToken,
		&row.CacheCreate,
		&row.CacheRead,
		&row.ActualCost,
	)
	s.Require().NoError(err)
	s.Equal(int64(1), row.Requests)
	s.Equal(int64(11), row.InputTokens)
	s.Equal(int64(13), row.OutputToken)
	s.Equal(int64(2), row.CacheCreate)
	s.Equal(int64(3), row.CacheRead)
	s.InDelta(rawIdx[grp.ID].ActualCost, row.ActualCost, 1e-9)
}

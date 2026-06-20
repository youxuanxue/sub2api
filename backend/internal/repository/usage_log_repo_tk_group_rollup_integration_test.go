//go:build integration

package repository

import (
	"time"

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
	acc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "grp-rollup-acc", Platform: service.PlatformAnthropic})
	grpA := mustCreateGroup(s.T(), s.client, &service.Group{Name: "grp-rollup-A", Platform: service.PlatformAnthropic})
	grpB := mustCreateGroup(s.T(), s.client, &service.Group{Name: "grp-rollup-B", Platform: service.PlatformOpenAI})

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

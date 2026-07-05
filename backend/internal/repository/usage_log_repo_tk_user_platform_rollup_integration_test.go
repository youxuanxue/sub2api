//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func indexByPlatform(rows []usagestats.PlatformUsage) map[string]usagestats.PlatformUsage {
	out := make(map[string]usagestats.PlatformUsage, len(rows))
	for _, r := range rows {
		out[r.Platform] = r
	}
	return out
}

// rollupParityCreateLog inserts a usage log pinned to a group (so the
// COALESCE(g.platform, a.platform) effective-platform path is exercised) at a
// chosen instant, and returns the contribution it makes to the reference totals.
func (s *UsageLogRepoSuite) rollupParityCreateLog(user *service.User, apiKey *service.APIKey, account *service.Account, groupID int64, in, out, cacheCreate, cacheRead int, cost float64, createdAt time.Time) {
	gid := groupID
	log := &service.UsageLog{
		UserID:              user.ID,
		APIKeyID:            apiKey.ID,
		AccountID:           account.ID,
		GroupID:             &gid,
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

// TestRollupParity_BatchAndRanking is the load-bearing equality test: it builds a
// fixture spanning multiple users, multiple effective platforms (including a
// group platform that OVERRIDES its account's platform via COALESCE), several
// completed days, zero-cost rows, and today's partial day; populates the
// per-(user, platform, day) rollup via AggregateRange; then asserts both
// rollup-backed read paths equal an independently hand-computed reference (the
// numbers the legacy raw-scan queries produced). Covers the default 30-day
// window and the narrow today-only window.
func (s *UsageLogRepoSuite) TestRollupParity_BatchAndRanking() {
	now := time.Now()
	today := timezone.Today()
	// Instants: two completed past days + today (after midnight).
	day5 := today.Add(-5 * 24 * time.Hour).Add(9 * time.Hour)
	day2 := today.Add(-2 * 24 * time.Hour).Add(14 * time.Hour)
	todayPoint := today.Add(3 * time.Hour)
	if todayPoint.After(now) {
		todayPoint = now.Add(-time.Minute)
	}

	user1 := mustCreateUser(s.T(), s.client, &service.User{Email: "rollup-u1@test.com"})
	user2 := mustCreateUser(s.T(), s.client, &service.User{Email: "rollup-u2@test.com"})
	key1 := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user1.ID, Key: "sk-rollup-1", Name: "k"})
	key2 := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user2.ID, Key: "sk-rollup-2", Name: "k"})
	accAnthropic := mustCreateAccount(s.T(), s.client, &service.Account{Name: "rollup-acc-anthropic", Platform: domain.PlatformAnthropic})
	accOpenAI := mustCreateAccount(s.T(), s.client, &service.Account{Name: "rollup-acc-openai", Platform: domain.PlatformOpenAI})
	// Anthropic account, but the group platform is openai -> COALESCE must report openai.
	grpAnthropic := mustCreateGroup(s.T(), s.client, &service.Group{Name: "rollup-grp-anthropic", Platform: domain.PlatformAnthropic})
	grpOpenAI := mustCreateGroup(s.T(), s.client, &service.Group{Name: "rollup-grp-openai", Platform: domain.PlatformOpenAI})

	// user1, anthropic platform:
	//   day5: 0.50 (10/20 tok), day2: 0.30 (10/20), today: 0.10 (10/20)
	//   day2 also one ZERO-cost row (5/5 tok) -> counts in requests/tokens, not in billed total
	s.rollupParityCreateLog(user1, key1, accAnthropic, grpAnthropic.ID, 10, 20, 0, 0, 0.50, day5)
	s.rollupParityCreateLog(user1, key1, accAnthropic, grpAnthropic.ID, 10, 20, 0, 0, 0.30, day2)
	s.rollupParityCreateLog(user1, key1, accAnthropic, grpAnthropic.ID, 5, 5, 0, 0, 0.00, day2)
	s.rollupParityCreateLog(user1, key1, accAnthropic, grpAnthropic.ID, 10, 20, 0, 0, 0.10, todayPoint)
	// user1, openai platform (anthropic account + openai group, COALESCE override):
	//   day5: 0.40 (10/20 + 2/3 cache)
	s.rollupParityCreateLog(user1, key1, accAnthropic, grpOpenAI.ID, 10, 20, 2, 3, 0.40, day5)

	// user2, openai platform:
	//   day2: 0.70, today: 0.20
	s.rollupParityCreateLog(user2, key2, accOpenAI, grpOpenAI.ID, 10, 20, 0, 0, 0.70, day2)
	s.rollupParityCreateLog(user2, key2, accOpenAI, grpOpenAI.ID, 10, 20, 0, 0, 0.20, todayPoint)

	// Populate the rollup over the completed-day span (today is read from raw).
	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	s.Require().NoError(aggRepo.AggregateRange(s.ctx, today.Add(-30*24*time.Hour), today), "AggregateRange")

	// --- GetBatchUserUsageStats (default 30d window) ---
	stats, err := s.repo.GetBatchUserUsageStats(s.ctx, []int64{user1.ID, user2.ID}, time.Time{}, time.Time{})
	s.Require().NoError(err)
	s.Require().Len(stats, 2)

	// user1 total: 0.50+0.30+0.00+0.10 (anthropic) + 0.40 (openai) = 1.30; today 0.10
	s.InDelta(1.30, stats[user1.ID].TotalActualCost, 1e-9)
	s.InDelta(0.10, stats[user1.ID].TodayActualCost, 1e-9)
	b1 := indexByPlatform(stats[user1.ID].ByPlatform)
	s.InDelta(0.90, b1[domain.PlatformAnthropic].TotalActualCost, 1e-9) // 0.50+0.30+0.10
	s.InDelta(0.10, b1[domain.PlatformAnthropic].TodayActualCost, 1e-9)
	s.InDelta(0.40, b1[domain.PlatformOpenAI].TotalActualCost, 1e-9, "COALESCE(g.platform,a.platform) override -> openai")
	s.InDelta(0.00, b1[domain.PlatformOpenAI].TodayActualCost, 1e-9)

	// user2 total: 0.70+0.20 = 0.90; today 0.20; openai only
	s.InDelta(0.90, stats[user2.ID].TotalActualCost, 1e-9)
	s.InDelta(0.20, stats[user2.ID].TodayActualCost, 1e-9)
	b2 := indexByPlatform(stats[user2.ID].ByPlatform)
	s.InDelta(0.90, b2[domain.PlatformOpenAI].TotalActualCost, 1e-9)
	s.InDelta(0.20, b2[domain.PlatformOpenAI].TodayActualCost, 1e-9)

	// --- GetBatchUserUsageStats: narrow today-only window ---
	narrow, err := s.repo.GetBatchUserUsageStats(s.ctx, []int64{user1.ID, user2.ID}, today, now.Add(time.Hour))
	s.Require().NoError(err)
	s.InDelta(0.10, narrow[user1.ID].TotalActualCost, 1e-9)
	s.InDelta(0.10, narrow[user1.ID].TodayActualCost, 1e-9)
	s.InDelta(0.20, narrow[user2.ID].TotalActualCost, 1e-9)
	s.InDelta(0.20, narrow[user2.ID].TodayActualCost, 1e-9)

	// --- GetUserSpendingRanking (default 30d window, includes today) ---
	resp, err := s.repo.GetUserSpendingRanking(s.ctx, today.Add(-30*24*time.Hour), now.Add(time.Hour), 12)
	s.Require().NoError(err)
	rank := make(map[int64]UserSpendingRankingItem)
	for _, it := range resp.Ranking {
		rank[it.UserID] = it
	}
	// user1: cost 1.30, requests 5 (incl. zero-cost), tokens:
	//   day5 30 + day2 30 + day2zero 10 + today 30 + openai(30+5cache) 35 = 135
	s.InDelta(1.30, rank[user1.ID].ActualCost, 1e-9)
	s.Equal(int64(5), rank[user1.ID].Requests, "zero-cost row must count")
	s.Equal(int64(135), rank[user1.ID].Tokens)
	s.Equal("rollup-u1@test.com", rank[user1.ID].Email)
	// user2: cost 0.90, requests 2, tokens 60
	s.InDelta(0.90, rank[user2.ID].ActualCost, 1e-9)
	s.Equal(int64(2), rank[user2.ID].Requests)
	s.Equal(int64(60), rank[user2.ID].Tokens)

	// Window totals across both users.
	s.InDelta(2.20, resp.TotalActualCost, 1e-9)
	s.Equal(int64(7), resp.TotalRequests)
	s.Equal(int64(195), resp.TotalTokens)

	// Ordering: user1 (1.30) before user2 (0.90).
	s.Require().GreaterOrEqual(len(resp.Ranking), 2)
	s.Equal(user1.ID, resp.Ranking[0].UserID)
}

// TestRollupParity_EqualsLegacyRawScan cross-checks the rollup path against a
// direct raw-usage_logs computation of the SAME semantics for a randomized-ish
// fixture, so the parity is asserted against an independent SQL reference rather
// than only hand-computed constants.
func (s *UsageLogRepoSuite) TestRollupParity_EqualsLegacyRawScan() {
	today := timezone.Today()
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "rollup-legacy@test.com"})
	key := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-rollup-legacy", Name: "k"})
	acc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "rollup-legacy-acc", Platform: domain.PlatformAnthropic})
	grp := mustCreateGroup(s.T(), s.client, &service.Group{Name: "rollup-legacy-grp", Platform: domain.PlatformAnthropic})

	// Spread rows across 4 completed days; the rollup must equal the raw window sum.
	for d := 1; d <= 4; d++ {
		ts := today.Add(time.Duration(-d) * 24 * time.Hour).Add(time.Duration(d) * time.Hour)
		s.rollupParityCreateLog(user, key, acc, grp.ID, d, d*2, 0, 0, float64(d)*0.11, ts)
	}

	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	start := today.Add(-30 * 24 * time.Hour)
	s.Require().NoError(aggRepo.AggregateRange(s.ctx, start, today))

	// Reference: legacy raw computation of total actual_cost over [start, today),
	// run through the same executor (tx) the repo uses so it sees the same data.
	var refCost float64
	var refReqs int64
	var refTokens int64
	rows, err := s.repo.sql.QueryContext(s.ctx, `
		SELECT COALESCE(SUM(actual_cost),0), COUNT(*),
		       COALESCE(SUM(input_tokens+output_tokens+cache_creation_tokens+cache_read_tokens),0)
		FROM usage_logs WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
	`, user.ID, start, today)
	s.Require().NoError(err)
	s.Require().True(rows.Next())
	s.Require().NoError(rows.Scan(&refCost, &refReqs, &refTokens))
	s.Require().NoError(rows.Close())

	resp, err := s.repo.GetUserSpendingRanking(s.ctx, start, today, 12)
	s.Require().NoError(err)
	var got UserSpendingRankingItem
	for _, it := range resp.Ranking {
		if it.UserID == user.ID {
			got = it
		}
	}
	s.InDelta(refCost, got.ActualCost, 1e-9, "rollup ranking cost must equal raw window sum")
	s.Equal(refReqs, got.Requests, "rollup ranking requests must equal raw window count")
	s.Equal(refTokens, got.Tokens, "rollup ranking tokens must equal raw window sum")

	// Batch total over the same [start, today) window (no today slice here).
	batch, err := s.repo.GetBatchUserUsageStats(s.ctx, []int64{user.ID}, start, today)
	s.Require().NoError(err)
	s.InDelta(refCost, batch[user.ID].TotalActualCost, 1e-9, "rollup batch total must equal raw window sum")
}

// TestRollupParity_ColdStartPartialCoverage locks the post-tk_034 cold-start case:
// the rollup is populated for only the most recent days (the shared aggregation
// watermark has already advanced, so the incremental feeder never backfills older
// days into the new table). The 30-day read MUST still equal a full raw scan,
// because completed days the rollup does not cover fall back to raw usage_logs.
// Without the coverage floor this test would undercount days 3..10.
func (s *UsageLogRepoSuite) TestRollupParity_ColdStartPartialCoverage() {
	today := timezone.Today()
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "rollup-coldstart@test.com"})
	key := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-rollup-coldstart", Name: "k"})
	acc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "rollup-coldstart-acc", Platform: domain.PlatformAnthropic})
	grp := mustCreateGroup(s.T(), s.client, &service.Group{Name: "rollup-coldstart-grp", Platform: domain.PlatformAnthropic})

	// Raw rows across 10 completed days.
	for d := 1; d <= 10; d++ {
		ts := today.Add(time.Duration(-d) * 24 * time.Hour).Add(time.Duration(d) * time.Hour)
		s.rollupParityCreateLog(user, key, acc, grp.ID, d, d*2, 0, 0, float64(d)*0.13, ts)
	}

	// Cold start: populate the rollup for ONLY the last 2 completed days (as
	// recomputeRecentDays with the default RecomputeDays=2 would on boot). Days
	// 3..10 are deliberately left out of the rollup.
	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	s.Require().NoError(aggRepo.AggregateRange(s.ctx, today.Add(-2*24*time.Hour), today))

	start := today.Add(-30 * 24 * time.Hour)
	// Reference: full raw scan over the whole window.
	var refCost float64
	var refReqs int64
	var refTokens int64
	rows, err := s.repo.sql.QueryContext(s.ctx, `
		SELECT COALESCE(SUM(actual_cost),0), COUNT(*),
		       COALESCE(SUM(input_tokens+output_tokens+cache_creation_tokens+cache_read_tokens),0)
		FROM usage_logs WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
	`, user.ID, start, today)
	s.Require().NoError(err)
	s.Require().True(rows.Next())
	s.Require().NoError(rows.Scan(&refCost, &refReqs, &refTokens))
	s.Require().NoError(rows.Close())

	resp, err := s.repo.GetUserSpendingRanking(s.ctx, start, today, 12)
	s.Require().NoError(err)
	var got UserSpendingRankingItem
	for _, it := range resp.Ranking {
		if it.UserID == user.ID {
			got = it
		}
	}
	s.InDelta(refCost, got.ActualCost, 1e-9, "cold-start ranking must equal full raw scan (uncovered days fall back to raw)")
	s.Equal(refReqs, got.Requests, "cold-start ranking requests must equal full raw count")
	s.Equal(refTokens, got.Tokens, "cold-start ranking tokens must equal full raw sum")

	batch, err := s.repo.GetBatchUserUsageStats(s.ctx, []int64{user.ID}, start, today)
	s.Require().NoError(err)
	s.InDelta(refCost, batch[user.ID].TotalActualCost, 1e-9, "cold-start batch total must equal full raw scan")
}

// TestRollupParity_EmptyEffectivePlatform locks the edge case where a completed
// day's effective platform is empty (account.platform = ” and group.platform =
// ”, a storable-but-near-impossible state since accounts.platform is NOT NULL
// without a non-empty CHECK). The legacy queries folded such rows into the user
// TOTAL (ranking groups only by user_id; batch sums every row into the total but
// only emits non-empty platforms in ByPlatform). The rollup must preserve that:
// the feeder coalesces empty platform to ” instead of dropping the row, and the
// reads fold ” into the total without emitting an empty ByPlatform entry.
func (s *UsageLogRepoSuite) TestRollupParity_EmptyEffectivePlatform() {
	today := timezone.Today()
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "rollup-empty@test.com"})
	key := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-rollup-empty", Name: "k"})
	acc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "rollup-empty-acc", Platform: domain.PlatformAnthropic})
	grp := mustCreateGroup(s.T(), s.client, &service.Group{Name: "rollup-empty-grp", Platform: domain.PlatformAnthropic})

	// Force both platforms empty so COALESCE(NULLIF(g.platform,''), a.platform) = ''.
	_, err := s.repo.sql.ExecContext(s.ctx, "UPDATE accounts SET platform = '' WHERE id = $1", acc.ID)
	s.Require().NoError(err)
	_, err = s.repo.sql.ExecContext(s.ctx, "UPDATE groups SET platform = '' WHERE id = $1", grp.ID)
	s.Require().NoError(err)

	// One billed row on a completed day -> served from the rollup, not raw.
	s.rollupParityCreateLog(user, key, acc, grp.ID, 7, 11, 0, 0, 0.55, today.Add(-3*24*time.Hour).Add(8*time.Hour))

	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	start := today.Add(-30 * 24 * time.Hour)
	s.Require().NoError(aggRepo.AggregateRange(s.ctx, start, today))

	// Ranking: the empty-platform row must contribute to the user total.
	resp, err := s.repo.GetUserSpendingRanking(s.ctx, start, today, 12)
	s.Require().NoError(err)
	var got UserSpendingRankingItem
	for _, it := range resp.Ranking {
		if it.UserID == user.ID {
			got = it
		}
	}
	s.InDelta(0.55, got.ActualCost, 1e-9, "empty-platform row must count in ranking total")
	s.Equal(int64(1), got.Requests)
	s.Equal(int64(18), got.Tokens) // 7+11

	// Batch: total includes the empty-platform row, ByPlatform omits the '' entry.
	batch, err := s.repo.GetBatchUserUsageStats(s.ctx, []int64{user.ID}, start, today)
	s.Require().NoError(err)
	s.InDelta(0.55, batch[user.ID].TotalActualCost, 1e-9, "empty-platform row must count in batch total")
	for _, p := range batch[user.ID].ByPlatform {
		s.NotEqual("", p.Platform, "ByPlatform must not contain an empty-platform entry")
	}
}

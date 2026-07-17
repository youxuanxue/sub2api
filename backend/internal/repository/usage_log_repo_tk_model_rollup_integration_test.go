//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func indexModelStats(rows []ModelStat) map[string]ModelStat {
	out := make(map[string]ModelStat, len(rows))
	for _, r := range rows {
		out[r.Model] = r
	}
	return out
}

func (s *UsageLogRepoSuite) modelRollupParityCreateLog(user *service.User, apiKey *service.APIKey, account *service.Account, requestedModel, storedModel string, in, out, cacheCreate, cacheRead int, cost float64, createdAt time.Time) {
	log := &service.UsageLog{
		UserID:              user.ID,
		APIKeyID:            apiKey.ID,
		AccountID:           account.ID,
		Model:               storedModel,
		RequestedModel:      requestedModel,
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

// TestModelStatsRollupWaitsForBackfillMarkerAndMatchesRaw locks the prod deploy
// safety property for usage_dashboard_model_daily: partial/stale rows from the
// forward-only aggregation watermark must not be trusted until the one-time
// historical rebuild has set the model backfill marker. Once the marker exists,
// completed days from the rollup plus today's raw tail must equal the legacy raw
// model distribution.
func (s *UsageLogRepoSuite) TestModelStatsRollupWaitsForBackfillMarkerAndMatchesRaw() {
	now := timezone.Now()
	today := timezone.Today()
	start := today.Add(-6 * 24 * time.Hour)
	day5 := today.Add(-5 * 24 * time.Hour).Add(9 * time.Hour)
	day2 := today.Add(-2 * 24 * time.Hour).Add(14 * time.Hour)
	todayPoint := today.Add(3 * time.Hour)
	if todayPoint.After(now) {
		todayPoint = now.Add(-time.Minute)
	}
	if todayPoint.Before(today) {
		todayPoint = today.Add(time.Second)
	}
	end := todayPoint.Add(time.Minute)

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "model-rollup@test.com"})
	key := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-model-rollup", Name: "k"})
	acc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "model-rollup-acc", Platform: service.PlatformAnthropic})

	s.modelRollupParityCreateLog(user, key, acc, "claude-sonnet-4-6", "upstream-sonnet", 10, 20, 1, 2, 0.50, day5)
	s.modelRollupParityCreateLog(user, key, acc, "claude-opus-4-6", "upstream-opus", 2, 3, 0, 1, 0.20, day2)
	s.modelRollupParityCreateLog(user, key, acc, "claude-sonnet-4-6", "upstream-sonnet", 5, 7, 0, 0, 0.10, todayPoint)

	// Simulate current prod before this fix: the rollup table has data, but there
	// is no marker proving that historical days were rebuilt from raw usage_logs.
	_, err := s.tx.ExecContext(s.ctx, `
		INSERT INTO usage_dashboard_model_daily (
			bucket_date,
			model,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			computed_at
		)
		VALUES ($1::date, $2, 999, 999, 999, 0, 0, 99, 88, 77, NOW())
		ON CONFLICT (bucket_date, model) DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			actual_cost = EXCLUDED.actual_cost,
			computed_at = EXCLUDED.computed_at
	`, day5.Format("2006-01-02"), "claude-sonnet-4-6")
	s.Require().NoError(err)

	ready, err := s.repo.modelDailyBackfilled(s.ctx)
	s.Require().NoError(err)
	s.False(ready, "model rollup fast path must wait for the full-history backfill marker")

	pre, err := s.repo.GetModelStatsWithFilters(s.ctx, start, end, 0, 0, 0, 0, nil, nil, nil)
	s.Require().NoError(err)
	preIdx := indexModelStats(pre)
	s.Equal(int64(2), preIdx["claude-sonnet-4-6"].Requests)
	s.Equal(int64(45), preIdx["claude-sonnet-4-6"].TotalTokens)
	s.InDelta(0.60, preIdx["claude-sonnet-4-6"].ActualCost, 1e-9)
	s.Equal(int64(1), preIdx["claude-opus-4-6"].Requests)
	s.Equal(int64(6), preIdx["claude-opus-4-6"].TotalTokens)

	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	s.Require().NoError(aggRepo.backfillModelDailyAllOnce(s.ctx))

	ready, err = s.repo.modelDailyBackfilled(s.ctx)
	s.Require().NoError(err)
	s.True(ready, "the marker should enable the production model stats rollup after rewrite")

	post, err := s.repo.GetModelStatsWithFilters(s.ctx, start, end, 0, 0, 0, 0, nil, nil, nil)
	s.Require().NoError(err)
	postIdx := indexModelStats(post)
	s.Require().Len(postIdx, len(preIdx))
	for model, want := range preIdx {
		got, ok := postIdx[model]
		s.Require().True(ok, "rollup path missing model=%s", model)
		s.Equal(want.Requests, got.Requests, "requests model=%s", model)
		s.Equal(want.InputTokens, got.InputTokens, "input_tokens model=%s", model)
		s.Equal(want.OutputTokens, got.OutputTokens, "output_tokens model=%s", model)
		s.Equal(want.CacheCreationTokens, got.CacheCreationTokens, "cache_creation_tokens model=%s", model)
		s.Equal(want.CacheReadTokens, got.CacheReadTokens, "cache_read_tokens model=%s", model)
		s.Equal(want.TotalTokens, got.TotalTokens, "total_tokens model=%s", model)
		s.InDelta(want.Cost, got.Cost, 1e-9, "cost model=%s", model)
		s.InDelta(want.ActualCost, got.ActualCost, 1e-9, "actual_cost model=%s", model)
		s.InDelta(want.AccountCost, got.AccountCost, 1e-9, "account_cost model=%s", model)
	}
}

//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *UsageLogRepoSuite) TestUS073_HistorySurvivesRawRetentionAndIsTenantScoped() {
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "history-bill@test.com"})
	other := mustCreateUser(s.T(), s.client, &service.User{Email: "other-history-bill@test.com"})
	// Deliberately create NO raw usage_logs; the financial history must survive
	// their removal and cannot fall back to an empty raw query.
	_, err := s.repo.sql.ExecContext(s.ctx, `UPDATE users SET created_at='2026-06-04T00:00:00Z' WHERE id IN ($1,$2)`, user.ID, other.ID)
	s.Require().NoError(err)
	_, err = s.repo.sql.ExecContext(s.ctx, `UPDATE usage_dashboard_user_platform_rollup_state SET timezone_name='UTC',success_metrics_version=1 WHERE id=1`)
	s.Require().NoError(err)
	_, err = s.repo.sql.ExecContext(s.ctx, `INSERT INTO usage_dashboard_user_platform_daily
 (bucket_date,user_id,platform,total_requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,actual_cost,computed_at)
 VALUES ('2026-06-04',$1,'openai',2,10,20,30,40,0.1000000001,now()),
 ('2026-06-05',$1,'openai',1,1,2,3,4,0.2000000002,now()),
 ('2026-06-05',$1,'',1,5,0,0,0,0,now()),
 ('2026-06-04',$2,'openai',999,999,999,999,999,999,now())`, user.ID, other.ID)
	s.Require().NoError(err)
	svc := service.NewUsageService(s.repo, nil, nil, nil)
	report, err := svc.GetHistoricalBilling(s.ctx, user.ID, usagestats.HistoricalBillingQuery{StartDate: "2026-06-04", EndDate: "2026-06-05", Granularity: "month"})
	s.Require().NoError(err)
	s.Equal("0.3000000003", report.Totals.ActualCost)
	s.Equal(int64(4), report.Totals.Requests)
	s.Equal(int64(115), report.Totals.TotalTokens)
	s.Equal("2026-06-04", report.AvailableStartDate)
	s.Equal("2026-06-05", report.AvailableEndDate)
	s.Equal("2026-06", report.Rows[0].Date)
	s.False(report.Totals.Provisional)
	s.WithinDuration(time.Now(), report.GeneratedAt, time.Minute)
	emptyPlatform := ""
	scoped, err := svc.GetHistoricalBilling(s.ctx, user.ID, usagestats.HistoricalBillingQuery{Platform: &emptyPlatform})
	s.Require().NoError(err)
	s.Equal(int64(1), scoped.Totals.Requests)
	s.Equal("0", scoped.Totals.ActualCost)
}

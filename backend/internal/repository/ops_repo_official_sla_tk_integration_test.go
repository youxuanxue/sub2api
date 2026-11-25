//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsBeijingOfficialSLA(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: "official-sla", Platform: service.PlatformOpenAI})
	user := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	account := mustCreateAccount(t, client, &service.Account{Name: "official-sla", Platform: service.PlatformOpenAI})
	t.Cleanup(func() {
		require.NoError(t, client.Account.DeleteOneID(account.ID).Exec(ctx))
		require.NoError(t, client.APIKey.DeleteOneID(key.ID).Exec(ctx))
		require.NoError(t, client.User.DeleteOneID(user.ID).Exec(ctx))
		require.NoError(t, client.Group.DeleteOneID(group.ID).Exec(ctx))
	})
	repo := NewOpsRepository(integrationDB).(*opsRepository)
	start := beijingTrialStart.Truncate(time.Hour).Add(-time.Minute)
	end := beijingTrialEnd.Truncate(time.Hour).Add(time.Hour + time.Minute)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_error_logs WHERE group_id=$1", group.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE api_key_id=$1", key.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_metrics_hourly WHERE bucket_start >= $1 AND bucket_start < $2", start.Truncate(time.Hour), end)
	})
	for _, at := range []time.Time{beijingTrialStart.Add(-time.Second), beijingTrialStart, beijingTrialEnd.Add(-time.Second), beijingTrialEnd} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs (user_id,api_key_id,account_id,group_id,model,created_at) VALUES ($1,$2,$3,$4,'example',$5)`, user.ID, key.ID, account.ID, group.ID, at)
		require.NoError(t, err)
	}
	for i, at := range []time.Time{start, beijingTrialStart, beijingTrialStart.Add(time.Hour), beijingTrialEnd, end.Add(-time.Second)} {
		owner := "platform"
		if i == 4 {
			owner = "client"
		}
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs (error_phase,error_type,status_code,error_owner,platform,group_id,created_at) VALUES ('internal','api_error',503,$1,'openai',$2,$3)`, owner, group.ID, at)
		require.NoError(t, err)
	}
	// Include both owners and directions: a final 502 after an upstream 400/429 is still a fault.
	for _, tc := range []struct {
		final, up int
		owner     string
		at        time.Time
	}{
		{400, 503, "platform", beijingTrialEnd},
		{400, 400, "provider", beijingTrialStart},
		{400, 0, "client", beijingTrialEnd},
		{429, 429, "platform", beijingTrialEnd},
		{429, 503, "provider", beijingTrialEnd},
		{502, 400, "provider", beijingTrialEnd},
		{502, 429, "provider", beijingTrialEnd},
	} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs(error_phase,error_type,status_code,upstream_status_code,error_owner,platform,group_id,created_at) VALUES ('upstream','api_error',$1,$2,$3,'openai',$4,$5)`, tc.final, tc.up, tc.owner, group.ID, tc.at)
		require.NoError(t, err)
	}
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, start.Truncate(time.Hour), utcCeilToHour(end)))
	filter := &service.OpsDashboardFilter{StartTime: start, EndTime: end, Platform: "openai", GroupID: &group.ID}
	for _, node := range []string{"cn-prod-app-a", "prod-app-cand", ""} {
		t.Setenv("TOKENKEY_NODE_NAME", node)
		for _, mode := range []service.OpsQueryMode{service.OpsQueryModeRaw, service.OpsQueryModePreagg, service.OpsQueryModeAuto} {
			filter.QueryMode = mode
			ov, err := repo.GetDashboardOverview(ctx, filter)
			require.NoError(t, err)
			// 4 usage + 5×503 + 7 upstream-shaped rows = 16 requests.
			// Operational SLA faults: 4 platform 503 + 2 final 400 (platform/provider) +
			// 2 final 429 (platform/provider) + 2 final 502 = 10; client rows stay out.
			require.EqualValues(t, 16, ov.RequestCountTotal)
			require.EqualValues(t, 10, ov.ErrorCountSLA)
			require.InDelta(t, 6.0/16, ov.SLA, 0.0001)
			if node == "" {
				require.Nil(t, ov.OfficialSLA)
				continue
			}
			require.NotNil(t, ov.OfficialSLA)
			if node == "prod-app-cand" {
				// Formal: drop final 400/429; keep 4×503 + 2×502 = 6 faults / 16.
				require.EqualValues(t, 16, ov.OfficialSLA.Requests)
				require.EqualValues(t, 6, ov.OfficialSLA.Faults)
				require.InDelta(t, 10.0/16, *ov.OfficialSLA.Rate, 1e-9)
				continue
			}
			// cn-prod excludes trial window: 2 usage + 3×503-shaped + 6 end-of-trial
			// upstream rows (one provider 400 stays inside the trial and drops out).
			require.EqualValues(t, 11, ov.OfficialSLA.Requests)
			require.EqualValues(t, 4, ov.OfficialSLA.Faults)
			require.NotNil(t, ov.OfficialSLA.Rate)
			require.InDelta(t, 7.0/11, *ov.OfficialSLA.Rate, 1e-9)
		}
	}
	t.Setenv("TOKENKEY_NODE_NAME", "cn-prod-app-b")
	filter.StartTime = beijingTrialStart
	filter.EndTime = beijingTrialEnd
	ov, err := repo.GetDashboardOverview(ctx, filter)
	require.NoError(t, err)
	require.NotNil(t, ov.OfficialSLA)
	require.Nil(t, ov.OfficialSLA.Rate)
	require.EqualValues(t, 0, ov.OfficialSLA.Requests)
	require.EqualValues(t, 5, ov.RequestCountTotal)
	// A historical partial-hour query must validate its containing hour too.
	at := end.Add(2 * time.Hour).Truncate(time.Hour).Add(time.Minute)
	hour := at.Truncate(time.Hour)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM ops_metrics_hourly WHERE bucket_start=$1", hour)
	})
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_logs (user_id,api_key_id,account_id,group_id,model,created_at) VALUES ($1,$2,$3,$4,'example',$5)`, user.ID, key.ID, account.ID, group.ID, at)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, owner string
		status      int
		rate        float64
	}{
		{"last fault", "provider", 502, .5},
		{"denominator only", "client", 400, 1},
		{"reclassified fault", "platform", 400, 1},
		{"429 rate limit", "platform", 429, 1},
		{"429 provider rate limit", "provider", 429, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs(error_phase,error_type,status_code,error_owner,platform,group_id,created_at) VALUES ('internal','api_error',$1,$2,'openai',$3,$4)`, tc.status, tc.owner, group.ID, at)
			require.NoError(t, err)
			require.NoError(t, repo.UpsertHourlyMetrics(ctx, hour, hour.Add(time.Hour)))
			for _, missing := range []bool{false, true} {
				if missing {
					_, err = integrationDB.ExecContext(ctx, `DELETE FROM ops_error_logs WHERE group_id=$1 AND created_at=$2`, group.ID, at)
					require.NoError(t, err)
				}
				for _, node := range []string{"prod-app-a", "cn-prod-app-b"} {
					t.Setenv("TOKENKEY_NODE_NAME", node)
					for _, span := range [][2]time.Time{{hour, hour.Add(time.Hour)}, {at, at.Add(time.Minute)}} {
						filter.StartTime, filter.EndTime = span[0], span[1]
						for _, mode := range []service.OpsQueryMode{service.OpsQueryModeRaw, service.OpsQueryModePreagg, service.OpsQueryModeAuto} {
							filter.QueryMode = mode
							ov, err := repo.GetDashboardOverview(ctx, filter)
							require.NoError(t, err)
							require.NotNil(t, ov.OfficialSLA)
							require.Equal(t, missing, ov.OfficialSLA.Incomplete, "%s %s %s", node, mode, span)
							if missing {
								require.Nil(t, ov.OfficialSLA.Rate)
							} else {
								require.EqualValues(t, 2, ov.OfficialSLA.Requests)
								require.NotNil(t, ov.OfficialSLA.Rate)
								require.Equal(t, tc.rate, *ov.OfficialSLA.Rate)
							}
						}
					}
				}
			}
		})
	}
}

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
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, start.Truncate(time.Hour), end.Truncate(time.Hour)))
	filter := &service.OpsDashboardFilter{StartTime: start, EndTime: end, Platform: "openai", GroupID: &group.ID}
	for _, node := range []string{"cn-prod-app-a", "prod-app-cand", ""} {
		t.Setenv("TOKENKEY_NODE_NAME", node)
		for _, mode := range []service.OpsQueryMode{service.OpsQueryModeRaw, service.OpsQueryModePreagg, service.OpsQueryModeAuto} {
			filter.QueryMode = mode
			ov, err := repo.GetDashboardOverview(ctx, filter)
			require.NoError(t, err)
			require.EqualValues(t, 9, ov.RequestCountTotal)
			require.EqualValues(t, 4, ov.ErrorCountSLA)
			require.InDelta(t, 5.0/9, ov.SLA, 0.0001)
			if node != "cn-prod-app-a" {
				require.Nil(t, ov.OfficialSLA)
				continue
			}
			require.NotNil(t, ov.OfficialSLA)
			require.EqualValues(t, 5, ov.OfficialSLA.Requests)
			require.EqualValues(t, 2, ov.OfficialSLA.Faults)
			require.NotNil(t, ov.OfficialSLA.Rate)
			require.InDelta(t, 0.6, *ov.OfficialSLA.Rate, 1e-9)
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
	require.EqualValues(t, 4, ov.RequestCountTotal)
}

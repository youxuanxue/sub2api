//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestUpstreamExclExcludesRecoveredRows 验证 upstream_excl(=upstream_error_rate 分子)
// 只统计最终失败(status_code>=400)的 provider 行:
//
// us7 P0 2026-06-10T13:40Z 教训——thinking-block 签名 400 经 signature_preempt 剥离重试
// 恢复成 final 200 的行(owner=provider, status_code=200)曾被计入分子,5 分钟窗 60/218≈27.5%
// 触发假 P0。修复后这类已恢复行不再驱动 upstream_error_rate,但 429/529 观测计数器保持原语义。
//
// 同一过滤表达式存在于 raw dashboard / trends / preagg(hourly) / metrics collector 四处;
// 本测试覆盖 repository 三处(collector 在 service 包,表达式相同)。
func TestUpstreamExclExcludesRecoveredRows(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_metrics_hourly")

	repo := NewOpsRepository(integrationDB).(*opsRepository)

	windowStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	windowEnd := windowStart.Add(time.Hour)
	at := windowStart.Add(5 * time.Minute)

	insert := func(statusCode int, owner string, upstreamStatus *int) {
		_, err := integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (
				error_phase, error_type, severity, status_code,
				error_owner, upstream_status_code, created_at
			) VALUES ('upstream', 'upstream_error', 'error', $1, $2, $3, $4)`,
			statusCode, owner, upstreamStatus, at,
		)
		require.NoError(t, err)
	}
	intPtr := func(v int) *int { return &v }

	// 1) us7 P0 驱动行:provider 错误已恢复成 final 200(upstream_status_code 为 NULL,
	//    如 thinking_blocks_stripped)——不得计入 upstream_excl。
	insert(200, "provider", nil)
	// 2) 恢复行变体:上游 400 被重试恢复成 final 200——同样不得计入。
	insert(200, "provider", intPtr(400))
	// 3) 最终失败的 provider 502——唯一应计入 upstream_excl 的行。
	insert(502, "provider", nil)
	// 4) 客户端归属的 502(context canceled)——owner 过滤排除(#628 语义)。
	insert(502, "client", nil)
	// 5) 最终失败的 provider 429——进 upstream_429 桶,不进 excl。
	insert(429, "provider", intPtr(429))

	filter := &service.OpsDashboardFilter{
		StartTime: windowStart,
		EndTime:   windowEnd,
		QueryMode: service.OpsQueryModeRaw,
	}

	// ── raw dashboard(告警评估器走的路径)─────────────────────────────────
	overview, err := repo.GetDashboardOverview(ctx, filter)
	require.NoError(t, err)
	require.NotNil(t, overview)
	require.EqualValues(t, 1, overview.UpstreamErrorCountExcl429529,
		"recovered-to-200 provider rows must not count toward upstream_excl")
	require.EqualValues(t, 1, overview.Upstream429Count)
	require.EqualValues(t, 3, overview.ErrorCountSLA, "final >=400 rows: provider 502 + client 502 + provider 429")

	// ── trends ───────────────────────────────────────────────────────────
	trend, err := repo.GetErrorTrend(ctx, filter, 3600)
	require.NoError(t, err)
	var trendExcl, trend429 int64
	for _, p := range trend.Points {
		trendExcl += p.UpstreamErrorCountExcl429529
		trend429 += p.Upstream429Count
	}
	require.EqualValues(t, 1, trendExcl)
	require.EqualValues(t, 1, trend429)

	// ── preagg hourly(dashboard preagg 模式的数据源)──────────────────────
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, windowStart, windowEnd))
	var preaggExcl, preagg429 int64
	err = integrationDB.QueryRowContext(ctx, `
		SELECT upstream_error_count_excl_429_529, upstream_429_count
		FROM ops_metrics_hourly
		WHERE bucket_start = $1 AND platform IS NULL AND group_id IS NULL`,
		windowStart,
	).Scan(&preaggExcl, &preagg429)
	require.NoError(t, err)
	require.EqualValues(t, 1, preaggExcl)
	require.EqualValues(t, 1, preagg429)
}

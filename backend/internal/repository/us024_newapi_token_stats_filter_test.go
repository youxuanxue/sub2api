package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// US-024 AC-001 (正向): platform=newapi 时跳过 ul.model LIKE 'gpt%' 过滤，使
// newapi 下游非 gpt 前缀的模型 (moonshot-v1-32k / claude-shape / 自定义渠道) 也能
// 出现在 OpenAI Token Stats 卡片里。
func TestUS024_OpenAITokenStats_NewAPI_SkipsGPTPrefixFilter(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	filter := &service.OpsOpenAITokenStatsFilter{
		TimeRange: "1d",
		StartTime: start,
		EndTime:   end,
		Platform:  " NewAPI ",
		TopN:      5,
	}

	// COUNT 阶段：SQL 中**不应**含 LIKE 'gpt%'。负面断言：使用 (?:.(?!LIKE 'gpt%'))
	// 风格不便表达，改为正向断言完整 where 形状（含 platform 占位但无 model 子句）。
	mock.ExpectQuery(`FROM stats`).
		WithArgs(start, end, "newapi").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))

	rows := sqlmock.NewRows([]string{
		"model",
		"request_count",
		"avg_tokens_per_sec",
		"avg_first_token_ms",
		"total_output_tokens",
		"avg_duration_ms",
		"requests_with_first_token",
	}).
		AddRow("moonshot-v1-32k", int64(15), 31.2, 110.0, int64(2400), int64(700), int64(15)).
		AddRow("claude-3-5-sonnet", int64(10), 22.0, 95.0, int64(1800), int64(820), int64(10))

	mock.ExpectQuery(`ORDER BY request_count DESC, model ASC\s+LIMIT \$4`).
		WithArgs(start, end, "newapi", 5).
		WillReturnRows(rows)

	resp, err := repo.GetOpenAITokenStats(context.Background(), filter)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "newapi", resp.Platform)
	require.Len(t, resp.Items, 2)
	require.Equal(t, "moonshot-v1-32k", resp.Items[0].Model, "non-gpt prefix model must surface for newapi")
	require.Equal(t, "claude-3-5-sonnet", resp.Items[1].Model)

	require.NoError(t, mock.ExpectationsWereMet())
}

// US-024 AC-002 (回归保护): platform != newapi 时仍应保留 gpt% 前缀过滤，OpenAI
// 卡片不应被这次修复污染成"所有 OpenAI 模型大杂烩"。
func TestUS024_OpenAITokenStats_OpenAI_KeepsGPTPrefixFilter(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}

	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	filter := &service.OpsOpenAITokenStatsFilter{
		TimeRange: "1h",
		StartTime: start,
		EndTime:   end,
		Platform:  "openai",
		TopN:      3,
	}

	// COUNT 阶段：必须含 LIKE 'gpt%' 子句。正则需要转义 %。
	mock.ExpectQuery(`ul\.model LIKE 'gpt%'.*FROM stats`).
		WithArgs(start, end, "openai").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))

	rows := sqlmock.NewRows([]string{
		"model",
		"request_count",
		"avg_tokens_per_sec",
		"avg_first_token_ms",
		"total_output_tokens",
		"avg_duration_ms",
		"requests_with_first_token",
	}).
		AddRow("gpt-4o", int64(7), 18.0, 130.0, int64(1200), int64(900), int64(7))

	mock.ExpectQuery(`ul\.model LIKE 'gpt%'.*ORDER BY request_count DESC, model ASC\s+LIMIT \$4`).
		WithArgs(start, end, "openai", 3).
		WillReturnRows(rows)

	resp, err := repo.GetOpenAITokenStats(context.Background(), filter)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "openai", resp.Platform)
	require.Len(t, resp.Items, 1)
	require.Equal(t, "gpt-4o", resp.Items[0].Model)

	require.NoError(t, mock.ExpectationsWereMet())
}

// US-024 AC-003 (回归保护): platform 为空 (无过滤) 时仍保留 gpt% 子句，因为该卡片
// 的语义是 "OpenAI-shape GPT 模型总览"。如果连 platform 都没传，等价于看全 GPT
// 模型 (即"原始行为")，绝不能扩展为全部模型。
func TestUS024_OpenAITokenStats_NoPlatform_KeepsGPTPrefixFilter(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}

	start := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	filter := &service.OpsOpenAITokenStatsFilter{
		TimeRange: "1h",
		StartTime: start,
		EndTime:   end,
		Platform:  "",
		TopN:      3,
	}

	mock.ExpectQuery(`ul\.model LIKE 'gpt%'.*FROM stats`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))

	mock.ExpectQuery(`ul\.model LIKE 'gpt%'.*ORDER BY request_count DESC, model ASC\s+LIMIT \$3`).
		WithArgs(start, end, 3).
		WillReturnRows(sqlmock.NewRows([]string{
			"model",
			"request_count",
			"avg_tokens_per_sec",
			"avg_first_token_ms",
			"total_output_tokens",
			"avg_duration_ms",
			"requests_with_first_token",
		}))

	resp, err := repo.GetOpenAITokenStats(context.Background(), filter)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "", resp.Platform)
	require.Empty(t, resp.Items)

	require.NoError(t, mock.ExpectationsWereMet())
}

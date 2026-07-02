package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryCreateSyncRequestTypeAndLegacyFields(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	createdAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	log := &service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      "req-1",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		InputTokens:    10,
		OutputTokens:   20,
		TotalCost:      1,
		ActualCost:     1,
		BillingType:    service.BillingTypeBalance,
		RequestType:    service.RequestTypeWSV2,
		Stream:         false,
		OpenAIWSMode:   false,
		CreatedAt:      createdAt,
	}

	mock.ExpectQuery("INSERT INTO usage_logs").
		WithArgs(
			log.UserID,
			log.APIKeyID,
			log.AccountID,
			log.RequestID,
			log.Model,
			log.RequestedModel,
			sqlmock.AnyArg(), // upstream_model
			sqlmock.AnyArg(), // group_id
			sqlmock.AnyArg(), // subscription_id
			log.InputTokens,
			log.OutputTokens,
			log.CacheCreationTokens,
			log.CacheReadTokens,
			log.CacheCreation5mTokens,
			log.CacheCreation1hTokens,
			log.ImageOutputTokens,
			log.ImageOutputCost,
			log.InputCost,
			log.OutputCost,
			log.CacheCreationCost,
			log.CacheReadCost,
			log.TotalCost,
			log.ActualCost,
			log.RateMultiplier,
			log.AccountRateMultiplier,
			log.BillingType,
			int16(service.RequestTypeWSV2),
			true,
			true,
			sqlmock.AnyArg(), // duration_ms
			sqlmock.AnyArg(), // first_token_ms
			sqlmock.AnyArg(), // user_agent
			sqlmock.AnyArg(), // ip_address
			log.ImageCount,
			sqlmock.AnyArg(), // image_size
			sqlmock.AnyArg(), // image_input_size
			sqlmock.AnyArg(), // image_output_size
			sqlmock.AnyArg(), // image_size_source
			sqlmock.AnyArg(), // image_size_breakdown
			sqlmock.AnyArg(), // service_tier
			sqlmock.AnyArg(), // reasoning_effort
			sqlmock.AnyArg(), // inbound_endpoint
			sqlmock.AnyArg(), // upstream_endpoint
			log.CacheTTLOverridden,
			sqlmock.AnyArg(), // channel_id
			sqlmock.AnyArg(), // model_mapping_chain
			sqlmock.AnyArg(), // billing_tier
			sqlmock.AnyArg(), // billing_mode
			sqlmock.AnyArg(), // account_stats_cost
			sqlmock.AnyArg(), // video_duration_seconds
			createdAt,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(99), createdAt))

	inserted, err := repo.Create(context.Background(), log)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, int64(99), log.ID)
	require.Nil(t, log.ServiceTier)
	require.Equal(t, service.RequestTypeWSV2, log.RequestType)
	require.True(t, log.Stream)
	require.True(t, log.OpenAIWSMode)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryCreate_PersistsServiceTier(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	createdAt := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	serviceTier := "priority"
	log := &service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      "req-service-tier",
		Model:          "gpt-5.4",
		RequestedModel: "gpt-5.4",
		ServiceTier:    &serviceTier,
		CreatedAt:      createdAt,
	}

	mock.ExpectQuery("INSERT INTO usage_logs").
		WithArgs(
			log.UserID,
			log.APIKeyID,
			log.AccountID,
			log.RequestID,
			log.Model,
			log.RequestedModel,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			log.InputTokens,
			log.OutputTokens,
			log.CacheCreationTokens,
			log.CacheReadTokens,
			log.CacheCreation5mTokens,
			log.CacheCreation1hTokens,
			log.ImageOutputTokens,
			log.ImageOutputCost,
			log.InputCost,
			log.OutputCost,
			log.CacheCreationCost,
			log.CacheReadCost,
			log.TotalCost,
			log.ActualCost,
			log.RateMultiplier,
			log.AccountRateMultiplier,
			log.BillingType,
			int16(service.RequestTypeSync),
			false,
			false,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			log.ImageCount,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(), // image_input_size
			sqlmock.AnyArg(), // image_output_size
			sqlmock.AnyArg(), // image_size_source
			sqlmock.AnyArg(), // image_size_breakdown
			serviceTier,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			log.CacheTTLOverridden,
			sqlmock.AnyArg(), // channel_id
			sqlmock.AnyArg(), // model_mapping_chain
			sqlmock.AnyArg(), // billing_tier
			sqlmock.AnyArg(), // billing_mode
			sqlmock.AnyArg(), // account_stats_cost
			sqlmock.AnyArg(), // video_duration_seconds
			createdAt,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(100), createdAt))

	inserted, err := repo.Create(context.Background(), log)
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildUsageLogBestEffortInsertQuery_IncludesRequestedModelColumn(t *testing.T) {
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      "req-best-effort-query",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		CreatedAt:      time.Date(2025, 1, 3, 12, 0, 0, 0, time.UTC),
	})

	query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})

	require.Contains(t, query, "INSERT INTO usage_logs (")
	require.Contains(t, query, "\n\t\t\tmodel,\n\t\t\trequested_model,\n\t\t\tupstream_model,")
	require.Contains(t, query, "\n\t\t\trequest_id,\n\t\t\tmodel,\n\t\t\trequested_model,\n\t\t\tupstream_model,")
	require.Len(t, args, len(prepared.args))
	require.Equal(t, prepared.args[5], args[5])
}

func TestExecUsageLogInsertNoResult_PersistsRequestedModel(t *testing.T) {
	db, mock := newSQLMock(t)
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      "req-best-effort-exec",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		CreatedAt:      time.Date(2025, 1, 4, 12, 0, 0, 0, time.UTC),
	})

	mock.ExpectExec("INSERT INTO usage_logs").
		WithArgs(anySliceToDriverValues(prepared.args)...).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := execUsageLogInsertNoResult(context.Background(), db, prepared)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareUsageLogInsert_ArgCountMatchesTypes(t *testing.T) {
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      "req-arg-count",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		CreatedAt:      time.Date(2025, 1, 5, 12, 0, 0, 0, time.UTC),
	})

	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
}

func TestPrepareUsageLogInsert_PersistsImageSizeMetadata(t *testing.T) {
	imageSize := "4K"
	inputSize := "1024x1024"
	outputSize := "3840x2160"
	source := "output"
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:             1,
		APIKeyID:           2,
		AccountID:          3,
		RequestID:          "req-image-metadata",
		Model:              "gpt-image-2",
		RequestedModel:     "gpt-image-2",
		ImageCount:         2,
		ImageSize:          &imageSize,
		ImageInputSize:     &inputSize,
		ImageOutputSize:    &outputSize,
		ImageSizeSource:    &source,
		ImageSizeBreakdown: map[string]int{"1K": 1, "4K": 1},
		CreatedAt:          time.Date(2025, 1, 6, 12, 0, 0, 0, time.UTC),
	})

	require.Equal(t, sql.NullString{String: imageSize, Valid: true}, prepared.args[34])
	require.Equal(t, sql.NullString{String: inputSize, Valid: true}, prepared.args[35])
	require.Equal(t, sql.NullString{String: outputSize, Valid: true}, prepared.args[36])
	require.Equal(t, sql.NullString{String: source, Valid: true}, prepared.args[37])
	breakdownJSON, ok := prepared.args[38].(string)
	require.True(t, ok)
	require.JSONEq(t, `{"1K":1,"4K":1}`, breakdownJSON)
}

func TestCoalesceTrimmedString(t *testing.T) {
	require.Equal(t, "fallback", coalesceTrimmedString(sql.NullString{}, "fallback"))
	require.Equal(t, "fallback", coalesceTrimmedString(sql.NullString{Valid: true, String: "   "}, "fallback"))
	require.Equal(t, "value", coalesceTrimmedString(sql.NullString{Valid: true, String: "value"}, "fallback"))
}

func TestAppendUsageLogBillingModeWhereCondition(t *testing.T) {
	tests := []struct {
		name          string
		billingMode   string
		wantCondition string
	}{
		{
			name:          "image includes legacy image rows",
			billingMode:   string(service.BillingModeImage),
			wantCondition: "(billing_mode = $1 OR COALESCE(image_count, 0) > 0)",
		},
		{
			name:          "token includes legacy non-image rows",
			billingMode:   string(service.BillingModeToken),
			wantCondition: "(billing_mode = $1 OR ((billing_mode IS NULL OR billing_mode = '') AND COALESCE(image_count, 0) <= 0))",
		},
		{
			name:          "per request remains exact",
			billingMode:   string(service.BillingModePerRequest),
			wantCondition: "billing_mode = $1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conditions, args := appendUsageLogBillingModeWhereCondition(nil, nil, tt.billingMode)
			require.Equal(t, []string{tt.wantCondition}, conditions)
			require.Equal(t, []any{tt.billingMode}, args)
		})
	}
}

func anySliceToDriverValues(values []any) []driver.Value {
	out := make([]driver.Value, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func TestUsageLogRepositoryListWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	requestType := int16(service.RequestTypeWSV2)
	stream := false
	filters := usagestats.UsageLogFilters{
		RequestType: &requestType,
		Stream:      &stream,
		ExactTotal:  true,
	}

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM usage_logs WHERE \\(request_type = \\$1 OR \\(request_type = 0 AND openai_ws_mode = TRUE\\)\\)").
		WithArgs(requestType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT .* FROM usage_logs WHERE \\(request_type = \\$1 OR \\(request_type = 0 AND openai_ws_mode = TRUE\\)\\) ORDER BY id DESC LIMIT \\$2 OFFSET \\$3").
		WithArgs(requestType, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	logs, page, err := repo.ListWithFilters(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
	require.NoError(t, err)
	require.Empty(t, logs)
	require.NotNil(t, page)
	require.Equal(t, int64(0), page.Total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetUsageTrendWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(service.RequestTypeStream)
	stream := true

	mock.ExpectQuery("AND \\(request_type = \\$3 OR \\(request_type = 0 AND stream = TRUE AND openai_ws_mode = FALSE\\)\\)").
		WithArgs(start, end, requestType).
		WillReturnRows(sqlmock.NewRows([]string{"date", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost"}))

	trend, err := repo.GetUsageTrendWithFilters(context.Background(), start, end, "day", 0, 0, 0, 0, "", &requestType, &stream, nil)
	require.NoError(t, err)
	require.Empty(t, trend)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetModelStatsWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(service.RequestTypeWSV2)
	stream := false

	mock.ExpectQuery("AND \\(request_type = \\$3 OR \\(request_type = 0 AND openai_ws_mode = TRUE\\)\\)").
		WithArgs(start, end, requestType).
		WillReturnRows(sqlmock.NewRows([]string{"model", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost", "account_cost"}))

	stats, err := repo.GetModelStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, &requestType, &stream, nil)
	require.NoError(t, err)
	require.Empty(t, stats)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetUserModelStatsUsesRequestedModel(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("(?s)SELECT\\s+COALESCE\\(NULLIF\\(TRIM\\(requested_model\\), ''\\), model\\) as model,.*WHERE created_at >= \\$1 AND created_at < \\$2\\s+AND user_id = \\$3.*GROUP BY COALESCE\\(NULLIF\\(TRIM\\(requested_model\\), ''\\), model\\) ORDER BY total_tokens DESC").
		WithArgs(start, end, int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}).AddRow("gpt-5.5", int64(2), int64(10), int64(20), int64(0), int64(0), int64(30), 0.1, 0.08, 0.07))

	stats, err := repo.GetUserModelStats(context.Background(), 7, start, end)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	require.Equal(t, "gpt-5.5", stats[0].Model)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersRequestedModelSource(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	filters := usagestats.UsageLogFilters{
		Model:             "gpt-5",
		ModelFilterSource: usagestats.ModelSourceRequested,
	}

	mock.ExpectQuery("FROM usage_logs\\s+WHERE COALESCE\\(NULLIF\\(TRIM\\(requested_model\\), ''\\), model\\) = \\$1").
		WithArgs("gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests",
			"total_input_tokens",
			"total_output_tokens",
			"total_cache_tokens",
			"total_cache_creation_tokens",
			"total_cache_read_tokens",
			"total_cost",
			"total_actual_cost",
			"total_account_cost",
			"avg_duration_ms",
		}).AddRow(int64(1), int64(2), int64(3), int64(4), int64(1), int64(3), 1.2, 1.0, 1.2, 20.0))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(inbound_endpoint\\), ''\\), 'unknown'\\) AS endpoint").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(upstream_endpoint\\), ''\\), 'unknown'\\) AS endpoint").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT CONCAT\\(").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.TotalRequests)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	requestType := int16(service.RequestTypeSync)
	stream := true
	filters := usagestats.UsageLogFilters{
		RequestType: &requestType,
		Stream:      &stream,
	}

	mock.ExpectQuery("FROM usage_logs\\s+WHERE \\(request_type = \\$1 OR \\(request_type = 0 AND stream = FALSE AND openai_ws_mode = FALSE\\)\\)").
		WithArgs(requestType).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests",
			"total_input_tokens",
			"total_output_tokens",
			"total_cache_tokens",
			"total_cache_creation_tokens",
			"total_cache_read_tokens",
			"total_cost",
			"total_actual_cost",
			"total_account_cost",
			"avg_duration_ms",
		}).AddRow(int64(1), int64(2), int64(3), int64(4), int64(1), int64(3), 1.2, 1.0, 1.2, 20.0))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(inbound_endpoint\\), ''\\), 'unknown'\\) AS endpoint").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), requestType).
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(upstream_endpoint\\), ''\\), 'unknown'\\) AS endpoint").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), requestType).
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT CONCAT\\(").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), requestType).
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.TotalRequests)
	require.Equal(t, int64(9), stats.TotalTokens)
	require.NotNil(t, stats.TotalAccountCost, "TotalAccountCost should always be returned")
	require.Equal(t, 1.2, *stats.TotalAccountCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetModelStatsAccountCostColumn(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("FROM usage_logs").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}).
			AddRow("claude-opus-4-6", int64(10), int64(100), int64(200), int64(5), int64(3), int64(308), 2.5, 2.0, 1.8).
			AddRow("claude-sonnet-4-6", int64(5), int64(50), int64(100), int64(0), int64(0), int64(150), 1.0, 0.8, 0.7))

	results, err := repo.GetModelStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "claude-opus-4-6", results[0].Model)
	require.Equal(t, 2.5, results[0].Cost)
	require.Equal(t, 2.0, results[0].ActualCost)
	require.Equal(t, 1.8, results[0].AccountCost)
	require.Equal(t, "claude-sonnet-4-6", results[1].Model)
	require.Equal(t, 0.7, results[1].AccountCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetModelStatsRollupRawTailGroupsByExpression(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	today := timezone.Today()
	start := today.Add(-2 * 24 * time.Hour)
	end := today.Add(2 * time.Hour)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM usage_dashboard_model_daily").
		WithArgs(modelDailyBackfillMarkerModel).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	mock.ExpectQuery("SELECT to_char\\(MIN\\(bucket_date\\), 'YYYY-MM-DD'\\) FROM usage_dashboard_model_daily").
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(start.Format("2006-01-02")))

	mock.ExpectQuery("FROM usage_dashboard_model_daily").
		WithArgs(start, today).
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}).AddRow("claude-sonnet-4-6", int64(20), int64(200), int64(400), int64(10), int64(5), int64(615), 4.0, 3.2, 2.8))

	mock.ExpectQuery("FROM usage_logs\\s+WHERE created_at >= \\$1 AND created_at < \\$2\\s+GROUP BY 1").
		WithArgs(today, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}).
			AddRow("claude-sonnet-4-6", int64(3), int64(30), int64(40), int64(2), int64(1), int64(73), 0.7, 0.6, 0.5).
			AddRow("claude-opus-4-6", int64(1), int64(10), int64(20), int64(0), int64(0), int64(30), 0.4, 0.3, 0.2))

	results, err := repo.GetModelStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "claude-sonnet-4-6", results[0].Model)
	require.Equal(t, int64(23), results[0].Requests)
	require.Equal(t, int64(688), results[0].TotalTokens)
	require.InDelta(t, 4.7, results[0].Cost, 1e-9)
	require.InDelta(t, 3.8, results[0].ActualCost, 1e-9)
	require.InDelta(t, 3.3, results[0].AccountCost, 1e-9)
	require.Equal(t, "claude-opus-4-6", results[1].Model)
	require.Equal(t, int64(30), results[1].TotalTokens)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetModelStatsRollupReturnsEmptyWithoutRawFallback(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM usage_dashboard_model_daily").
		WithArgs(modelDailyBackfillMarkerModel).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	mock.ExpectQuery("SELECT to_char\\(MIN\\(bucket_date\\), 'YYYY-MM-DD'\\) FROM usage_dashboard_model_daily").
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow("2025-01-01"))

	mock.ExpectQuery("FROM usage_dashboard_model_daily").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}))

	results, err := repo.GetModelStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, nil, nil, nil)
	require.NoError(t, err)
	require.Empty(t, results)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationModelBackfillDefersWhenDeadlineTooShort(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM usage_dashboard_model_daily").
		WithArgs(modelDailyBackfillMarkerModel).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	require.NoError(t, repo.backfillModelDailyAllOnce(ctx))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetGroupStatsAccountCostColumn(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("FROM usage_logs").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "group_name", "requests", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}).
			AddRow(int64(1), "azure-cc", int64(100), int64(5000), 10.0, 8.5, 7.2).
			AddRow(int64(2), "max", int64(50), int64(2000), 5.0, 4.0, 3.5))

	results, err := repo.GetGroupStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, int64(1), results[0].GroupID)
	require.Equal(t, "azure-cc", results[0].GroupName)
	require.Equal(t, 10.0, results[0].Cost)
	require.Equal(t, 8.5, results[0].ActualCost)
	require.Equal(t, 7.2, results[0].AccountCost)
	require.Equal(t, int64(2), results[1].GroupID)
	require.Equal(t, 3.5, results[1].AccountCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetGroupStatsRollupFallbackUntilMetricsBackfill(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db, db: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM usage_dashboard_group_daily WHERE group_id = 0 AND bucket_date = DATE '1970-01-02'\\)").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectQuery("FROM usage_logs").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "group_name", "requests", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}).AddRow(int64(1), "azure-cc", int64(100), int64(5000), 10.0, 8.5, 7.2))

	results, err := repo.GetGroupStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, int64(5000), results[0].TotalTokens)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetGroupStatsRollupMergesCompletedDaysAndRawTail(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db, db: db}

	today := timezone.Today()
	start := today.Add(-2 * 24 * time.Hour)
	end := today.Add(2 * time.Hour)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM usage_dashboard_group_daily WHERE group_id = 0 AND bucket_date = DATE '1970-01-02'\\)").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT to_char\\(MIN\\(bucket_date\\), 'YYYY-MM-DD'\\) FROM usage_dashboard_group_daily WHERE bucket_date > DATE '1970-01-02'").
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(start.Format("2006-01-02")))

	mock.ExpectQuery("FROM usage_dashboard_group_daily gd").
		WithArgs(start, today).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "group_name", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "cost", "actual_cost", "account_cost",
		}).
			AddRow(int64(1), "azure-cc", int64(10), int64(100), int64(200), int64(5), int64(3), 2.0, 1.5, 1.2).
			AddRow(int64(0), "", int64(4), int64(8), int64(12), int64(1), int64(1), 0.3, 0.2, 0.2).
			AddRow(int64(2), "max", int64(2), int64(20), int64(30), int64(0), int64(0), 0.5, 0.4, 0.3))

	mock.ExpectQuery("FROM usage_logs ul").
		WithArgs(today, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "group_name", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "cost", "actual_cost", "account_cost",
		}).
			AddRow(int64(1), "azure-cc", int64(3), int64(30), int64(40), int64(2), int64(1), 0.7, 0.6, 0.5).
			AddRow(int64(3), "other", int64(1), int64(5), int64(7), int64(0), int64(0), 0.2, 0.1, 0.1))

	results, err := repo.GetGroupStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 4)
	require.Equal(t, int64(1), results[0].GroupID)
	require.Equal(t, "azure-cc", results[0].GroupName)
	require.Equal(t, int64(13), results[0].Requests)
	require.Equal(t, int64(381), results[0].TotalTokens)
	require.InDelta(t, 2.7, results[0].Cost, 1e-9)
	require.InDelta(t, 2.1, results[0].ActualCost, 1e-9)
	require.InDelta(t, 1.7, results[0].AccountCost, 1e-9)
	require.Equal(t, int64(2), results[1].GroupID)
	require.Equal(t, int64(50), results[1].TotalTokens)
	require.Equal(t, int64(0), results[2].GroupID)
	require.Equal(t, "", results[2].GroupName)
	require.Equal(t, int64(22), results[2].TotalTokens)
	require.Equal(t, int64(3), results[3].GroupID)
	require.Equal(t, int64(12), results[3].TotalTokens)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersAlwaysReturnsAccountCost(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	// No AccountID filter set - TotalAccountCost should still be returned
	filters := usagestats.UsageLogFilters{}

	mock.ExpectQuery("FROM usage_logs").
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens",
			"total_cache_tokens", "total_cache_creation_tokens", "total_cache_read_tokens",
			"total_cost", "total_actual_cost",
			"total_account_cost", "avg_duration_ms",
		}).AddRow(int64(50), int64(1000), int64(2000), int64(100), int64(60), int64(40), 15.0, 12.5, 11.0, 100.0))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(inbound_endpoint\\)").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(upstream_endpoint\\)").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT CONCAT\\(").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.NotNil(t, stats.TotalAccountCost, "TotalAccountCost must always be returned, even without AccountID filter")
	require.Equal(t, 11.0, *stats.TotalAccountCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersCanSkipEndpointStats(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	filters := usagestats.UsageLogFilters{SkipEndpointStats: true}

	mock.ExpectQuery("FROM usage_logs").
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens",
			"total_cache_tokens", "total_cache_creation_tokens", "total_cache_read_tokens",
			"total_cost", "total_actual_cost",
			"total_account_cost", "avg_duration_ms",
		}).AddRow(int64(50), int64(1000), int64(2000), int64(100), int64(60), int64(40), 15.0, 12.5, 11.0, 100.0))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(50), stats.TotalRequests)
	require.Empty(t, stats.Endpoints)
	require.Empty(t, stats.UpstreamEndpoints)
	require.Empty(t, stats.EndpointPaths)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersCanSkipSummary(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	filters := usagestats.UsageLogFilters{SkipSummary: true}

	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(inbound_endpoint\\)").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}).
			AddRow("/v1/messages", int64(2), int64(30), 0.2, 0.1))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(upstream_endpoint\\)").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT CONCAT\\(").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Zero(t, stats.TotalRequests)
	require.Len(t, stats.Endpoints, 1)
	require.Equal(t, "/v1/messages", stats.Endpoints[0].Endpoint)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersCanLoadSingleEndpointSource(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	filters := usagestats.UsageLogFilters{
		SkipSummary:         true,
		EndpointStatsSource: usagestats.EndpointSourceUpstream,
	}

	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(upstream_endpoint\\)").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}).
			AddRow("https://upstream.example/v1/messages", int64(2), int64(30), 0.2, 0.1))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Empty(t, stats.Endpoints)
	require.Len(t, stats.UpstreamEndpoints, 1)
	require.Equal(t, "https://upstream.example/v1/messages", stats.UpstreamEndpoints[0].Endpoint)
	require.Empty(t, stats.EndpointPaths)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersUsesHourlyRollupForUnfilteredSummary(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db, db: db}

	start := time.Date(2026, 6, 22, 0, 30, 0, 0, time.UTC)
	end := time.Date(2026, 6, 23, 8, 15, 0, 0, time.UTC)
	filters := usagestats.UsageLogFilters{StartTime: &start, EndTime: &end, SkipEndpointStats: true}

	mock.ExpectQuery("SELECT MIN\\(bucket_start\\) FROM usage_dashboard_hourly").
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
	mock.ExpectQuery("SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark").
		WillReturnRows(sqlmock.NewRows([]string{"last_aggregated_at"}).AddRow(time.Date(2026, 6, 23, 8, 5, 0, 0, time.UTC)))

	mock.ExpectQuery("FROM usage_dashboard_hourly").
		WithArgs(time.Date(2026, 6, 22, 1, 0, 0, 0, time.UTC), time.Date(2026, 6, 23, 8, 0, 0, 0, time.UTC)).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_tokens",
			"total_cost", "total_actual_cost", "total_account_cost", "total_duration_ms",
		}).AddRow(int64(100), int64(1000), int64(2000), int64(300), 10.0, 8.0, 7.0, int64(50000)))

	mock.ExpectQuery("FROM usage_logs\\s+WHERE created_at >= \\$1 AND created_at < \\$2").
		WithArgs(start, time.Date(2026, 6, 22, 1, 0, 0, 0, time.UTC)).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_tokens",
			"total_cost", "total_actual_cost", "total_account_cost", "total_duration_ms",
		}).AddRow(int64(2), int64(10), int64(20), int64(3), 0.2, 0.15, 0.1, int64(1000)))

	mock.ExpectQuery("FROM usage_logs\\s+WHERE created_at >= \\$1 AND created_at < \\$2").
		WithArgs(time.Date(2026, 6, 23, 8, 0, 0, 0, time.UTC), end).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_tokens",
			"total_cost", "total_actual_cost", "total_account_cost", "total_duration_ms",
		}).AddRow(int64(3), int64(30), int64(40), int64(7), 0.3, 0.25, 0.2, int64(1500)))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(105), stats.TotalRequests)
	require.Equal(t, int64(1040), stats.TotalInputTokens)
	require.Equal(t, int64(2060), stats.TotalOutputTokens)
	require.Equal(t, int64(310), stats.TotalCacheTokens)
	require.InDelta(t, 10.5, stats.TotalCost, 1e-9)
	require.InDelta(t, 8.4, stats.TotalActualCost, 1e-9)
	require.NotNil(t, stats.TotalAccountCost)
	require.InDelta(t, 7.3, *stats.TotalAccountCost, 1e-9)
	require.InDelta(t, float64(52500)/105, stats.AverageDurationMs, 1e-9)
	require.Empty(t, stats.Endpoints)
	require.Empty(t, stats.UpstreamEndpoints)
	require.Empty(t, stats.EndpointPaths)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersHourlyRollupFloorKeepsPreFloorRaw(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db, db: db}

	start := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 22, 6, 30, 0, 0, time.UTC)
	filters := usagestats.UsageLogFilters{StartTime: &start, EndTime: &end, SkipEndpointStats: true}

	mock.ExpectQuery("SELECT MIN\\(bucket_start\\) FROM usage_dashboard_hourly").
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)))
	mock.ExpectQuery("SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark").
		WillReturnRows(sqlmock.NewRows([]string{"last_aggregated_at"}).AddRow(time.Date(2026, 6, 22, 6, 5, 0, 0, time.UTC)))

	mock.ExpectQuery("FROM usage_dashboard_hourly").
		WithArgs(time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC), time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC)).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_tokens",
			"total_cost", "total_actual_cost", "total_account_cost", "total_duration_ms",
		}).AddRow(int64(30), int64(300), int64(400), int64(50), 3.0, 2.0, 1.5, int64(15000)))

	mock.ExpectQuery("FROM usage_logs\\s+WHERE created_at >= \\$1 AND created_at < \\$2").
		WithArgs(start, time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_tokens",
			"total_cost", "total_actual_cost", "total_account_cost", "total_duration_ms",
		}).AddRow(int64(7), int64(70), int64(80), int64(9), 0.7, 0.6, 0.5, int64(3500)))

	mock.ExpectQuery("FROM usage_logs\\s+WHERE created_at >= \\$1 AND created_at < \\$2").
		WithArgs(time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC), end).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_tokens",
			"total_cost", "total_actual_cost", "total_account_cost", "total_duration_ms",
		}).AddRow(int64(2), int64(20), int64(30), int64(4), 0.2, 0.1, 0.05, int64(1000)))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(39), stats.TotalRequests)
	require.Equal(t, int64(390), stats.TotalInputTokens)
	require.Equal(t, int64(510), stats.TotalOutputTokens)
	require.Equal(t, int64(63), stats.TotalCacheTokens)
	require.InDelta(t, 3.9, stats.TotalCost, 1e-9)
	require.InDelta(t, 2.7, stats.TotalActualCost, 1e-9)
	require.NotNil(t, stats.TotalAccountCost)
	require.InDelta(t, 2.05, *stats.TotalAccountCost, 1e-9)
	require.InDelta(t, float64(19500)/39, stats.AverageDurationMs, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersFilteredSummaryFallsBackToRaw(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db, db: db}

	start := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	filters := usagestats.UsageLogFilters{UserID: 42, StartTime: &start, EndTime: &end, SkipEndpointStats: true}

	mock.ExpectQuery("FROM usage_logs").
		WithArgs(int64(42), start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens",
			"total_cache_tokens", "total_cache_creation_tokens", "total_cache_read_tokens",
			"total_cost", "total_actual_cost",
			"total_account_cost", "avg_duration_ms",
		}).AddRow(int64(4), int64(40), int64(50), int64(6), int64(4), int64(2), 0.4, 0.3, 0.2, 123.0))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(4), stats.TotalRequests)
	require.Equal(t, int64(96), stats.TotalTokens)
	require.NotNil(t, stats.TotalAccountCost)
	require.InDelta(t, 0.2, *stats.TotalAccountCost, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetUserSpendingRanking(t *testing.T) {
	// Pin UTC so the day-aligned window below has no partial-boundary raw
	// remainder regardless of the host's local timezone (the rollup window
	// decomposition floors/ceils to the server timezone).
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	// A historical, day-aligned window entirely before today: the rollup-backed
	// path (usage_log_repo_tk_user_platform_rollup.go) serves it wholly from
	// usage_dashboard_user_platform_daily (no raw remainder), then enriches each
	// row with the user email and assembles window totals + top-N ordering in Go.
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	// Rollup aggregate per user, deliberately out of rank order to prove the Go
	// sort (cost DESC, tokens DESC, user_id ASC).
	rollupRows := sqlmock.NewRows([]string{"user_id", "actual_cost", "total_requests", "tokens"}).
		AddRow(int64(3), 4.25, int64(5), int64(300)).
		AddRow(int64(1), 12.5, int64(8), int64(800)).
		AddRow(int64(2), 12.5, int64(9), int64(900))
	// Coverage floor (userPlatformRollupFloorDay): the rollup covers from the
	// window start or earlier, so the whole historical window is served from the
	// rollup with no raw fallback (matches the assertions below).
	mock.ExpectQuery("to_char\\(MIN\\(bucket_date\\)").
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow("2025-01-01"))

	mock.ExpectQuery("FROM usage_dashboard_user_platform_daily").
		WithArgs(start, end).
		WillReturnRows(rollupRows)

	// Email enrichment for the users seen in the window.
	emailRows := sqlmock.NewRows([]string{"id", "email"}).
		AddRow(int64(1), "alpha@example.com").
		AddRow(int64(2), "beta@example.com").
		AddRow(int64(3), "gamma@example.com")
	mock.ExpectQuery("SELECT id, COALESCE\\(email").
		WillReturnRows(emailRows)

	got, err := repo.GetUserSpendingRanking(context.Background(), start, end, 12)
	require.NoError(t, err)
	require.Equal(t, &usagestats.UserSpendingRankingResponse{
		Ranking: []usagestats.UserSpendingRankingItem{
			// cost tie (12.5) broken by tokens DESC: user 2 (900) before user 1 (800).
			{UserID: 2, Email: "beta@example.com", ActualCost: 12.5, Requests: 9, Tokens: 900},
			{UserID: 1, Email: "alpha@example.com", ActualCost: 12.5, Requests: 8, Tokens: 800},
			{UserID: 3, Email: "gamma@example.com", ActualCost: 4.25, Requests: 5, Tokens: 300},
		},
		TotalActualCost: 29.25,
		TotalRequests:   22,
		TotalTokens:     2000,
	}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetUserUsageTrendRollupSelectsTopUsersByTokens(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("to_char\\(MIN\\(bucket_date\\)").
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow("2025-01-01"))

	// Total ranking rows are deliberately ordered to expose the bug: user 3 has
	// the highest spend but far fewer tokens. Legacy GetUserUsageTrend chooses
	// top users by token volume, so limit=2 must select users 1 and 2.
	mock.ExpectQuery("FROM usage_dashboard_user_platform_daily").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "actual_cost", "total_requests", "tokens"}).
			AddRow(int64(3), 99.0, int64(1), int64(100)).
			AddRow(int64(1), 1.0, int64(10), int64(1000)).
			AddRow(int64(2), 2.0, int64(9), int64(900)))

	mock.ExpectQuery("FROM usage_dashboard_user_platform_daily").
		WillReturnRows(sqlmock.NewRows([]string{"date", "user_id", "cost", "actual_cost", "requests", "tokens"}).
			AddRow("2025-01-01", int64(1), 1.0, 1.0, int64(10), int64(1000)).
			AddRow("2025-01-01", int64(2), 2.0, 2.0, int64(9), int64(900)))

	mock.ExpectQuery("SELECT id, COALESCE\\(email").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "username"}).
			AddRow(int64(1), "alpha@example.com", "alpha").
			AddRow(int64(2), "beta@example.com", "beta"))

	got, err := repo.GetUserUsageTrend(context.Background(), start, end, "day", 2)
	require.NoError(t, err)
	require.Equal(t, []UserUsageTrendPoint{
		{
			Date:       "2025-01-01",
			UserID:     1,
			Email:      "alpha@example.com",
			Username:   "alpha",
			Requests:   10,
			Tokens:     1000,
			Cost:       1,
			ActualCost: 1,
		},
		{
			Date:       "2025-01-01",
			UserID:     2,
			Email:      "beta@example.com",
			Username:   "beta",
			Requests:   9,
			Tokens:     900,
			Cost:       2,
			ActualCost: 2,
		},
	}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildRequestTypeFilterConditionLegacyFallback(t *testing.T) {
	tests := []struct {
		name      string
		request   int16
		wantWhere string
		wantArg   int16
	}{
		{
			name:      "sync_with_legacy_fallback",
			request:   int16(service.RequestTypeSync),
			wantWhere: "(request_type = $3 OR (request_type = 0 AND stream = FALSE AND openai_ws_mode = FALSE))",
			wantArg:   int16(service.RequestTypeSync),
		},
		{
			name:      "stream_with_legacy_fallback",
			request:   int16(service.RequestTypeStream),
			wantWhere: "(request_type = $3 OR (request_type = 0 AND stream = TRUE AND openai_ws_mode = FALSE))",
			wantArg:   int16(service.RequestTypeStream),
		},
		{
			name:      "ws_v2_with_legacy_fallback",
			request:   int16(service.RequestTypeWSV2),
			wantWhere: "(request_type = $3 OR (request_type = 0 AND openai_ws_mode = TRUE))",
			wantArg:   int16(service.RequestTypeWSV2),
		},
		{
			name:      "invalid_request_type_normalized_to_unknown",
			request:   int16(99),
			wantWhere: "request_type = $3",
			wantArg:   int16(service.RequestTypeUnknown),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args := buildRequestTypeFilterCondition(3, tt.request)
			require.Equal(t, tt.wantWhere, where)
			require.Equal(t, []any{tt.wantArg}, args)
		})
	}
}

type usageLogScannerStub struct {
	values []any
}

func (s usageLogScannerStub) Scan(dest ...any) error {
	if len(dest) != len(s.values) {
		return fmt.Errorf("scan arg count mismatch: got %d want %d", len(dest), len(s.values))
	}
	for i := range dest {
		dv := reflect.ValueOf(dest[i])
		if dv.Kind() != reflect.Ptr {
			return fmt.Errorf("dest[%d] is not pointer", i)
		}
		dv.Elem().Set(reflect.ValueOf(s.values[i]))
	}
	return nil
}

func TestScanUsageLogRequestTypeAndLegacyFallback(t *testing.T) {
	t.Run("image_size_metadata_is_scanned", func(t *testing.T) {
		now := time.Now().UTC()
		log, err := scanUsageLog(usageLogScannerStub{values: []any{
			int64(4),
			int64(13),
			int64(23),
			int64(33),
			sql.NullString{Valid: true, String: "req-image-metadata"},
			"gpt-image-2",
			sql.NullString{Valid: true, String: "gpt-image-2"},
			sql.NullString{},
			sql.NullInt64{},
			sql.NullInt64{},
			0, 0, 0, 0, 0, 0,
			0, 0.0, // image_output_tokens, image_output_cost
			0.0, 0.0, 0.0, 0.0, 0.8, 0.8,
			1.0,
			sql.NullFloat64{},
			int16(service.BillingTypeBalance),
			int16(service.RequestTypeSync),
			false,
			false,
			sql.NullInt64{},
			sql.NullInt64{},
			sql.NullString{},
			sql.NullString{},
			2,
			sql.NullString{Valid: true, String: "4K"},
			sql.NullString{Valid: true, String: "1024x1024"},
			sql.NullString{Valid: true, String: "3840x2160"},
			sql.NullString{Valid: true, String: "output"},
			sql.NullString{Valid: true, String: `{"4K":2}`},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			false,
			sql.NullInt64{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullFloat64{},
			sql.NullInt64{}, // video_duration_seconds
			now,
		}})
		require.NoError(t, err)
		require.Equal(t, 2, log.ImageCount)
		require.NotNil(t, log.ImageSize)
		require.Equal(t, "4K", *log.ImageSize)
		require.NotNil(t, log.ImageInputSize)
		require.Equal(t, "1024x1024", *log.ImageInputSize)
		require.NotNil(t, log.ImageOutputSize)
		require.Equal(t, "3840x2160", *log.ImageOutputSize)
		require.NotNil(t, log.ImageSizeSource)
		require.Equal(t, "output", *log.ImageSizeSource)
		require.Equal(t, map[string]int{"4K": 2}, log.ImageSizeBreakdown)
	})

	t.Run("request_type_ws_v2_overrides_legacy", func(t *testing.T) {
		now := time.Now().UTC()
		log, err := scanUsageLog(usageLogScannerStub{values: []any{
			int64(1),  // id
			int64(10), // user_id
			int64(20), // api_key_id
			int64(30), // account_id
			sql.NullString{Valid: true, String: "req-1"},
			"gpt-5", // model
			sql.NullString{Valid: true, String: "gpt-5"}, // requested_model
			sql.NullString{},  // upstream_model
			sql.NullInt64{},   // group_id
			sql.NullInt64{},   // subscription_id
			1,                 // input_tokens
			2,                 // output_tokens
			3,                 // cache_creation_tokens
			4,                 // cache_read_tokens
			5,                 // cache_creation_5m_tokens
			6,                 // cache_creation_1h_tokens
			0,                 // image_output_tokens
			0.0,               // image_output_cost
			0.1,               // input_cost
			0.2,               // output_cost
			0.3,               // cache_creation_cost
			0.4,               // cache_read_cost
			1.0,               // total_cost
			0.9,               // actual_cost
			1.0,               // rate_multiplier
			sql.NullFloat64{}, // account_rate_multiplier
			int16(service.BillingTypeBalance),
			int16(service.RequestTypeWSV2),
			false, // legacy stream
			false, // legacy openai ws
			sql.NullInt64{},
			sql.NullInt64{},
			sql.NullString{},
			sql.NullString{},
			0,
			sql.NullString{},
			sql.NullString{}, // image_input_size
			sql.NullString{}, // image_output_size
			sql.NullString{}, // image_size_source
			sql.NullString{}, // image_size_breakdown
			sql.NullString{Valid: true, String: "priority"},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			false,
			sql.NullInt64{},   // channel_id
			sql.NullString{},  // model_mapping_chain
			sql.NullString{},  // billing_tier
			sql.NullString{},  // billing_mode
			sql.NullFloat64{}, // account_stats_cost
			sql.NullInt64{},   // video_duration_seconds
			now,
		}})
		require.NoError(t, err)
		require.NotNil(t, log.ServiceTier)
		require.Equal(t, "priority", *log.ServiceTier)
		require.Equal(t, service.RequestTypeWSV2, log.RequestType)
		require.True(t, log.Stream)
		require.True(t, log.OpenAIWSMode)
	})

	t.Run("request_type_unknown_falls_back_to_legacy", func(t *testing.T) {
		now := time.Now().UTC()
		log, err := scanUsageLog(usageLogScannerStub{values: []any{
			int64(2),
			int64(11),
			int64(21),
			int64(31),
			sql.NullString{Valid: true, String: "req-2"},
			"gpt-5",
			sql.NullString{Valid: true, String: "gpt-5"},
			sql.NullString{},
			sql.NullInt64{},
			sql.NullInt64{},
			1, 2, 3, 4, 5, 6,
			0, 0.0, // image_output_tokens, image_output_cost
			0.1, 0.2, 0.3, 0.4, 1.0, 0.9,
			1.0,
			sql.NullFloat64{},
			int16(service.BillingTypeBalance),
			int16(service.RequestTypeUnknown),
			true,
			false,
			sql.NullInt64{},
			sql.NullInt64{},
			sql.NullString{},
			sql.NullString{},
			0,
			sql.NullString{},
			sql.NullString{}, // image_input_size
			sql.NullString{}, // image_output_size
			sql.NullString{}, // image_size_source
			sql.NullString{}, // image_size_breakdown
			sql.NullString{Valid: true, String: "flex"},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			false,
			sql.NullInt64{},   // channel_id
			sql.NullString{},  // model_mapping_chain
			sql.NullString{},  // billing_tier
			sql.NullString{},  // billing_mode
			sql.NullFloat64{}, // account_stats_cost
			sql.NullInt64{},   // video_duration_seconds
			now,
		}})
		require.NoError(t, err)
		require.NotNil(t, log.ServiceTier)
		require.Equal(t, "flex", *log.ServiceTier)
		require.Equal(t, service.RequestTypeStream, log.RequestType)
		require.True(t, log.Stream)
		require.False(t, log.OpenAIWSMode)
	})

	t.Run("service_tier_is_scanned", func(t *testing.T) {
		now := time.Now().UTC()
		log, err := scanUsageLog(usageLogScannerStub{values: []any{
			int64(3),
			int64(12),
			int64(22),
			int64(32),
			sql.NullString{Valid: true, String: "req-3"},
			"gpt-5.4",
			sql.NullString{Valid: true, String: "gpt-5.4"},
			sql.NullString{},
			sql.NullInt64{},
			sql.NullInt64{},
			1, 2, 3, 4, 5, 6,
			0, 0.0, // image_output_tokens, image_output_cost
			0.1, 0.2, 0.3, 0.4, 1.0, 0.9,
			1.0,
			sql.NullFloat64{},
			int16(service.BillingTypeBalance),
			int16(service.RequestTypeSync),
			false,
			false,
			sql.NullInt64{},
			sql.NullInt64{},
			sql.NullString{},
			sql.NullString{},
			0,
			sql.NullString{},
			sql.NullString{}, // image_input_size
			sql.NullString{}, // image_output_size
			sql.NullString{}, // image_size_source
			sql.NullString{}, // image_size_breakdown
			sql.NullString{Valid: true, String: "priority"},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			false,
			sql.NullInt64{},   // channel_id
			sql.NullString{},  // model_mapping_chain
			sql.NullString{},  // billing_tier
			sql.NullString{},  // billing_mode
			sql.NullFloat64{}, // account_stats_cost
			sql.NullInt64{},   // video_duration_seconds
			now,
		}})
		require.NoError(t, err)
		require.NotNil(t, log.ServiceTier)
		require.Equal(t, "priority", *log.ServiceTier)
	})

}

func TestUsageLogRepositoryGetModelStatsWithUsageFiltersAppliesRequestedModelFilter(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := usagestats.UsageLogFilters{Model: "gpt-5"}

	mock.ExpectQuery("AND COALESCE\\(NULLIF\\(TRIM\\(requested_model\\), ''\\), model\\) = \\$3").
		WithArgs(start, end, "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}).AddRow("gpt-5", int64(1), int64(10), int64(20), int64(0), int64(0), int64(30), 0.1, 0.08, 0.07))

	results, err := repo.GetModelStatsWithUsageFiltersBySource(context.Background(), start, end, filters, usagestats.ModelSourceRequested)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "gpt-5", results[0].Model)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetGroupStatsWithUsageFiltersAppliesRequestedModelFilter(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := usagestats.UsageLogFilters{Model: "gpt-5"}

	mock.ExpectQuery("AND COALESCE\\(NULLIF\\(TRIM\\(ul.requested_model\\), ''\\), ul.model\\) = \\$3").
		WithArgs(start, end, "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "group_name", "requests", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}).AddRow(int64(1), "default", int64(1), int64(30), 0.1, 0.08, 0.07))

	results, err := repo.GetGroupStatsWithUsageFilters(context.Background(), start, end, filters)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, int64(1), results[0].GroupID)
	require.NoError(t, mock.ExpectationsWereMet())
}

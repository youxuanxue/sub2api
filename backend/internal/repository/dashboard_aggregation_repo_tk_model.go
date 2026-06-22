package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// TK: per-(requested-model, day) rollup feeder for the admin dashboard model-stats
// widget. Mirrors dashboard_aggregation_repo_tk_user_platform.go.

func (r *dashboardAggregationRepository) deleteModelDailyRange(ctx context.Context, dayStart, dayEnd time.Time) error {
	_, err := r.sql.ExecContext(ctx,
		"DELETE FROM usage_dashboard_model_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date",
		dayStart, dayEnd,
	)
	return err
}

func (r *dashboardAggregationRepository) upsertModelDailyAggregates(ctx context.Context, dayStart, dayEnd time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH per_row AS (
			SELECT
				(ul.created_at AT TIME ZONE $3)::date AS bucket_date,
				COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model) AS model,
				ul.input_tokens AS input_tokens,
				ul.output_tokens AS output_tokens,
				ul.cache_creation_tokens AS cache_creation_tokens,
				ul.cache_read_tokens AS cache_read_tokens,
				ul.total_cost AS total_cost,
				ul.actual_cost AS actual_cost,
				COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1) AS account_cost_row
			FROM usage_logs ul
			WHERE ul.created_at >= $1 AND ul.created_at < $2
		),
		rolled AS (
			SELECT
				bucket_date,
				model,
				COUNT(*) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(account_cost_row), 0) AS account_cost
			FROM per_row
			GROUP BY bucket_date, model
		)
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
		SELECT
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
			NOW()
		FROM rolled
		ON CONFLICT (bucket_date, model)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, dayStart, dayEnd, tzName)
	return err
}

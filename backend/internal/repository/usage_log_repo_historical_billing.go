package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetHistoricalBillingSnapshot reads metadata and aggregates in one statement so
// a concurrent feeder cannot change the snapshot between metadata and rows.
func (r *usageLogRepository) GetHistoricalBillingSnapshot(ctx context.Context, userID int64) (*usagestats.HistoricalBillingSnapshot, error) {
	rows, err := r.sql.QueryContext(ctx, `
 SELECT u.created_at, COALESCE(s.timezone_name, ''), statement_timestamp(),
        to_char(d.bucket_date, 'YYYY-MM-DD'), d.platform, d.total_requests,
        d.input_tokens, d.output_tokens, d.cache_creation_tokens, d.cache_read_tokens,
        d.actual_cost::text, d.computed_at
 FROM users u
 LEFT JOIN usage_dashboard_user_platform_rollup_state s ON s.id = 1
 LEFT JOIN usage_dashboard_user_platform_daily d ON d.user_id = u.id
 WHERE u.id = $1
 ORDER BY d.bucket_date, d.platform
 LIMIT 50001`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := &usagestats.HistoricalBillingSnapshot{Rows: []usagestats.HistoricalBillingRow{}}
	found := false
	for rows.Next() {
		found = true
		var date, platform, cost sql.NullString
		var requests, input, output, creation, read sql.NullInt64
		var computed sql.NullTime
		if err := rows.Scan(&result.RegisteredAt, &result.Timezone, &result.GeneratedAt,
			&date, &platform, &requests, &input, &output, &creation, &read, &cost, &computed); err != nil {
			return nil, err
		}
		if date.Valid {
			result.Rows = append(result.Rows, usagestats.HistoricalBillingRow{
				Date: date.String, Platform: platform.String, Requests: requests.Int64,
				InputTokens: input.Int64, OutputTokens: output.Int64,
				CacheCreationTokens: creation.Int64, CacheReadTokens: read.Int64,
				ActualCost: cost.String, ComputedAt: computed.Time,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, service.ErrUserNotFound
	}
	if len(result.Rows) > 50000 {
		return nil, fmt.Errorf("historical billing exceeds supported snapshot size")
	}
	return result, nil
}
